/*
Copyright 2021.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controllers

import (
	"context"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	nodesv1 "github.com/283713406/k8s-operator/node-pool-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/api/node/v1beta1"
)

// NodePoolReconciler reconciles a NodePool object
type NodePoolReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

const nodeFinalizer = "node.finalizers.node-pool.kylin.org"

//+kubebuilder:rbac:groups=nodes.kylin.org,resources=nodepools,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=nodes.kylin.org,resources=nodepools/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=nodes.kylin.org,resources=nodepools/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the NodePool object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.8.3/pkg/reconcile
func (r *NodePoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	_ = log.FromContext(ctx)

	// your logic here
	log.Log.Info("Reconcile is called")
	pool := &nodesv1.NodePool{}
	if err := r.Get(ctx, req.NamespacedName, pool); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	log.Log.Info("find nodepool", ":", pool.Name)

	var nodes corev1.NodeList

	err := r.List(ctx, &nodes, &client.ListOptions{LabelSelector: pool.NodeLabelSelector()})
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}

	// 进入预删除流程
	if !pool.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.nodeFinalizer(ctx, pool, nodes.Items)
	}

	// 如果删除时间戳为空说明现在不需要删除该数据，我们将 nodeFinalizer 加入到资源中
	if !containsString(pool.Finalizers, nodeFinalizer) {
		pool.Finalizers = append(pool.Finalizers, nodeFinalizer)
		if err := r.Client.Update(ctx, pool); err != nil {
			return ctrl.Result{}, err
		}
	}

	if len(nodes.Items) > 0 {
		log.Log.Info("find nodes, will merge data", "nodes", len(nodes.Items))
		pool.Status.Allocatable = corev1.ResourceList{}
		pool.Status.NodeCount = len(nodes.Items)
		for _, n := range nodes.Items {
			n := n
			err := r.Update(ctx, pool.Spec.ApplyNode(n))
			if err != nil {
				return ctrl.Result{}, err
			}

			for name, quantity := range n.Status.Allocatable {
				q, ok := pool.Status.Allocatable[name]
				if ok {
					q.Add(quantity)
					pool.Status.Allocatable[name] = q
					continue
				}
				pool.Status.Allocatable[name] = quantity
			}
		}
	}

	runtimeClass := &v1beta1.RuntimeClass{}
	err = r.Get(ctx, client.ObjectKeyFromObject(pool.RuntimeClass()), runtimeClass)
	if client.IgnoreNotFound(err) != nil {
		return ctrl.Result{}, err
	}

	// 如果不存在创建一个新的
	if runtimeClass.Name == "" {
		log.Log.Info("not find, will create runtimeClass")
		runtimeClass = pool.RuntimeClass()
		err = ctrl.SetControllerReference(pool, runtimeClass, r.Scheme)
		log.Log.Info("111111111111111111111111ss")
		if err != nil {
			return ctrl.Result{}, err
		}

		log.Log.Info("222222222222222222222222ss")
		err = r.Create(ctx, runtimeClass)
		if err != nil {
			return ctrl.Result{}, err
		}
	}

	log.Log.Info("33333333333333333333333333ss")
	// 如果存在则更新
	runtimeClass.Scheduling = pool.RuntimeClass().Scheduling
	runtimeClass.Handler = pool.RuntimeClass().Handler
	err = r.Client.Update(ctx, runtimeClass)
	if err != nil {
		return ctrl.Result{}, err
	}

	log.Log.Info("444444444444444444444ss")
	// 添加测试事件
	r.Recorder.Event(pool, corev1.EventTypeNormal, "test", "test")

	pool.Status.Status = 200
	err = r.Status().Update(ctx, pool)

	return ctrl.Result{}, nil
}

// 节点预删除逻辑
func (r *NodePoolReconciler) nodeFinalizer(ctx context.Context, pool *nodesv1.NodePool, nodes []corev1.Node) error {
	// 不为空就说明进入到预删除流程
	for _, n := range nodes {
		n := n

		// 更新节点的标签和污点信息
		err := r.Update(ctx, pool.Spec.CleanNode(n))
		if err != nil {
			return err
		}
	}

	// 预删除执行完毕，移除 nodeFinalizer
	pool.Finalizers = removeString(pool.Finalizers, nodeFinalizer)
	return r.Client.Update(ctx, pool)
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodePoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nodesv1.NodePool{}).
		Watches(&source.Kind{Type: &corev1.Node{}}, handler.Funcs{UpdateFunc: r.nodeUpdateHandler}).
		Complete(r)
}

func (r *NodePoolReconciler) nodeUpdateHandler(e event.UpdateEvent, q workqueue.RateLimitingInterface) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	oldPool, err := r.getNodePoolByLabels(ctx, e.ObjectOld.GetLabels())
	if err != nil {
		log.Log.Error(err, "get node pool err")
	}
	if oldPool != nil {
		q.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{Name: oldPool.Name},
		})
	}

	newPool, err := r.getNodePoolByLabels(ctx, e.ObjectNew.GetLabels())
	if err != nil {
		log.Log.Error(err, "get node pool err")
	}
	if newPool != nil {
		q.Add(reconcile.Request{
			NamespacedName: types.NamespacedName{Name: newPool.Name},
		})
	}
}

func (r *NodePoolReconciler) getNodePoolByLabels(ctx context.Context, labels map[string]string) (*nodesv1.NodePool, error) {
	pool := &nodesv1.NodePool{}
	for k := range labels {
		ss := strings.Split(k, "node-role.kubernetes.io/")
		if len(ss) != 2 {
			continue
		}
		err := r.Client.Get(ctx, types.NamespacedName{Name: ss[1]}, pool)
		if err == nil {
			return pool, nil
		}

		if client.IgnoreNotFound(err) != nil {
			return nil, err
		}
	}
	return nil, nil
}

func nodeReady(status corev1.NodeStatus) bool {
	for _, condition := range status.Conditions {
		if condition.Status == "True" && condition.Type == "Ready" {
			return true
		}
	}
	return false
}

// 辅助函数用于检查并从字符串切片中删除字符串。
func containsString(slice []string, s string) bool {
	for _, item := range slice {
		if item == s {
			return true
		}
	}
	return false
}

func removeString(slice []string, s string) (result []string) {
	for _, item := range slice {
		if item == s {
			continue
		}
		result = append(result, item)
	}
	return
}
