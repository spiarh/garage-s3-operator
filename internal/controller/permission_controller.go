/*
Copyright 2025 spiarh.

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

package controller

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	apierror "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	"github.com/spiarh/garage-s3-operator/api/v1alpha1"
)

// PermissionReconciler reconciles a Permission object
type PermissionReconciler struct {
	Log                  logr.Logger
	Scheme               *runtime.Scheme
	GarageManager        GarageAPI
	GarageManagerFactory GarageManagerFactory

	client.Client
}

// +kubebuilder:rbac:groups=s3.spiarh.fr,resources=garagepermissions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=s3.spiarh.fr,resources=garagepermissions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=s3.spiarh.fr,resources=garagepermissions/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
func (r *PermissionReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Log = logf.FromContext(ctx)

	r.Log.Info("starting reconciliation")

	perm := &v1alpha1.GaragePermission{}
	err := r.Get(ctx, req.NamespacedName, perm)
	if err != nil {
		if apierror.IsNotFound(err) {
			r.Log.V(3).Info("Permission not found, skipping reconciliation")
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	bucket, err := getBucket(ctx, r.Client, perm.Spec.BucketRef)
	if err != nil {
		if apierror.IsNotFound(err) {
			if !perm.DeletionTimestamp.IsZero() {
				r.Log.Info(fmt.Sprintf("Bucket %s not found, deleting Permission", perm.Spec.BucketRef.String()))

				if controllerutil.ContainsFinalizer(perm, finalizer) {
					controllerutil.RemoveFinalizer(perm, finalizer)
					if err := r.Update(ctx, perm); err != nil {
						return ctrl.Result{}, err
					}
				}
			}

			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	cluster, err := getCluster(ctx, r.Client, bucket.Spec.ClusterRef)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("unable to fetch GarageCluster: %w", err)
	}

	factory := r.GarageManagerFactory
	if factory == nil {
		factory = defaultGarageManagerFactory
	}

	r.GarageManager, err = factory(ctx, r.Client, r.Log, *cluster)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("unable to create garage manager: %w", err)
	}

	if !perm.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(perm, finalizer) {
			if err := r.reconcileDeletion(ctx, perm, bucket); err != nil {
				return ctrl.Result{}, err
			}

			controllerutil.RemoveFinalizer(perm, finalizer)
			if err := r.Update(ctx, perm); err != nil {
				return ctrl.Result{}, err
			}
		}

		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(perm, finalizer) {
		controllerutil.AddFinalizer(perm, finalizer)
		if err := r.Update(ctx, perm); err != nil {
			return ctrl.Result{}, err
		}
	}

	err = r.reconcile(ctx, bucket, perm)
	if err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

func (r *PermissionReconciler) reconcile(ctx context.Context, bucket *v1alpha1.GarageBucket, perm *v1alpha1.GaragePermission) error {
	key, err := getKey(ctx, r.Client, perm.Spec.KeyRef)
	if err != nil {
		if apierror.IsNotFound(err) {
			r.Log.Info("key not found, skipping permission grants")
			return nil
		}
		return fmt.Errorf("unable to fetch key: %w", err)
	}

	bucketInfo, resp, err := r.GarageManager.getBucket(bucket.TargetName())
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			r.Log.Info("target bucket not found, skipping permission grants")
			return nil
		}
		return fmt.Errorf("unable to get target bucket info: %w", err)
	}
	keyInfo, resp, err := r.GarageManager.getKeyByID(key.TargetName(), false)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			r.Log.Info("target key not found in Garage, skipping permission grants")
			return nil
		}
		return fmt.Errorf("unable to get target key info: %w", err)
	}

	expectedPermissions := perm.Spec.Permissions
	if expectedPermissions == nil {
		expectedPermissions = &v1alpha1.Permissions{
			Owner: true,
			Read:  true,
			Write: true,
		}
	}

	var bucketHasPermissions bool
	for _, b := range keyInfo.GetBuckets() {
		if slices.Contains(b.GetGlobalAliases(), bucket.TargetName()) {
			currentPermissions := v1alpha1.Permissions{
				Owner: b.Permissions.GetOwner(),
				Read:  b.Permissions.GetRead(),
				Write: b.Permissions.GetWrite(),
			}

			if currentPermissions == *expectedPermissions {
				bucketHasPermissions = true
			}
			break
		}
	}

	if !bucketHasPermissions {
		r.Log.Info("key has missing or wrong permissions")

		_, _, err := r.GarageManager.setPermissions(
			keyInfo.GetAccessKeyId(), bucketInfo.GetId(), expectedPermissions)
		if err != nil {
			return fmt.Errorf("unable to set permissions on bucket: %w", err)
		}
	}

	return nil
}

func (r *PermissionReconciler) reconcileDeletion(ctx context.Context, perm *v1alpha1.GaragePermission, bucket *v1alpha1.GarageBucket) error {
	key, err := getKey(ctx, r.Client, perm.Spec.KeyRef)
	if err != nil {
		if apierror.IsNotFound(err) {
			r.Log.Info("key not found, skipping permission revocation")
			return nil
		}
		return fmt.Errorf("unable to fetch key: %w", err)
	}

	bucketItem, err := r.GarageManager.findBucketInList(bucket.TargetName())
	if err != nil {
		return fmt.Errorf("unable to find bucket in Garage: %w", err)
	}
	if bucketItem == nil {
		r.Log.Info("target bucket not found in Garage, skipping permission revocation")
		return nil
	}

	keyItem, err := r.GarageManager.findKeyInList(key.TargetName())
	if err != nil {
		return fmt.Errorf("unable to find key in Garage: %w", err)
	}
	if keyItem == nil {
		r.Log.Info("target key not found in Garage, skipping permission revocation")
		return nil
	}

	_, err = r.GarageManager.denyAllPermissions(keyItem.Id, bucketItem.Id)
	if err != nil {
		return fmt.Errorf("unable to revoke permissions on bucket: %w", err)
	}

	r.Log.Info("all permissions revoked")
	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *PermissionReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.GaragePermission{}).
		Named("permission").
		Complete(r)
}
