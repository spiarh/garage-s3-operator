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
	"cmp"
	"context"
	"fmt"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	apierror "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/go-logr/logr"
	"github.com/spiarh/garage-s3-operator/api/v1alpha1"
)

// KeyReconciler reconciles a Key object
type KeyReconciler struct {
	Log                  logr.Logger
	Scheme               *runtime.Scheme
	GarageManager        GarageAPI
	GarageManagerFactory GarageManagerFactory

	client.Client
}

// +kubebuilder:rbac:groups=s3.spiarh.fr,resources=garageaccesskeys,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=s3.spiarh.fr,resources=garageaccesskeys/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=s3.spiarh.fr,resources=garageaccesskeys/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
func (r *KeyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	r.Log = logf.FromContext(ctx)

	r.Log.Info("starting reconciliation")

	key := &v1alpha1.GarageAccessKey{}
	err := r.Get(ctx, req.NamespacedName, key)
	if err != nil {
		if apierror.IsNotFound(err) {
			r.Log.V(3).Info("Key not found, skipping reconciliation")
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	cluster, err := getCluster(ctx, r.Client, key.Spec.ClusterRef)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("unable to fetch GarageCluster: %w", err)
	}

	factory := r.GarageManagerFactory
	if factory == nil {
		factory = defaultGarageManagerFactory
	}

	r.GarageManager, err = factory(ctx, r.Client, r.Log, *cluster)
	if err != nil {
		r.Log.Error(err, "unable to create garage manager")
		return ctrl.Result{}, err
	}

	if !key.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(key, finalizer) {
			if err := r.reconcileDeletion(ctx, key); err != nil {
				return ctrl.Result{}, err
			}

			controllerutil.RemoveFinalizer(key, finalizer)
			if err := r.Update(ctx, key); err != nil {
				return ctrl.Result{}, err
			}
		}

		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(key, finalizer) {
		controllerutil.AddFinalizer(key, finalizer)
		if err := r.Update(ctx, key); err != nil {
			return ctrl.Result{}, err
		}
	}

	err = r.reconcile(ctx, key)
	if err != nil {
		return ctrl.Result{}, err
	}

	r.Log.Info("reconciliation done")

	return ctrl.Result{}, nil
}

func (r *KeyReconciler) reconcile(ctx context.Context, key *v1alpha1.GarageAccessKey) error {
	keyName := key.TargetName()
	keyItem, err := r.GarageManager.findKeyInList(key.TargetName())
	if err != nil {
		return fmt.Errorf("unable to find key in Garage: %w", err)
	}

	if keyItem == nil {
		_, _, err := r.GarageManager.createKey(keyName)
		if err != nil {
			return fmt.Errorf("unable to create/update target key: %w", err)
		}
		r.Log.Info("target key created: " + keyName)
	}

	if key.Status.ID == "" {
		r.Log.Info("patch GarageKey status")

		keyInfo, _, err := r.GarageManager.getKeyByID(keyName, true)
		if err != nil {
			return fmt.Errorf("unable to get target key info: %w", err)
		}

		patch := client.MergeFrom(key.DeepCopy())
		key.Status.ID = keyInfo.AccessKeyId
		err = r.Status().Patch(ctx, key, patch)
		if err != nil {
			return fmt.Errorf("unable to patch GarageKey status: %w", err)
		}

		r.Log.Info("GarageKey status patched successfully")
	}

	r.Log.Info("reconcile access key Secret")
	err = r.reconcileSecret(ctx, key)
	if err != nil {
		return fmt.Errorf("unable to reconcile access key Secret: %w", err)
	}

	return nil
}

func (r *KeyReconciler) reconcileSecret(ctx context.Context, key *v1alpha1.GarageAccessKey) error {
	keyName := key.TargetName()
	keyInfo, _, err := r.GarageManager.getKeyByID(keyName, true)
	if err != nil {
		return fmt.Errorf("unable to get target key info: %w", err)
	}

	accessKeyID := keyInfo.GetAccessKeyId()
	secretAccessKey := keyInfo.GetSecretAccessKey()

	var format v1alpha1.Format
	if key.Spec.Secret != nil {
		format = cmp.Or(key.Spec.Secret.Format, v1alpha1.DefaultFormat)
	}

	secretName := cmp.Or(key.Spec.OverrideName, key.Name)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: key.Namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		err := controllerutil.SetOwnerReference(key, secret, r.Scheme)
		if err != nil {
			return err
		}

		secret.Type = corev1.SecretTypeOpaque
		secret.Data = getExpectedSecretContent(
			format, accessKeyID, secretAccessKey)

		return nil
	})
	if err != nil {
		r.Log.Error(err, "unable to create or update access key Secret")
		return err
	}

	switch op {
	case controllerutil.OperationResultCreated:
		r.Log.Info("access key Secret created")
	case controllerutil.OperationResultUpdated:
		r.Log.Info("access key Secret updated")
	case controllerutil.OperationResultNone:
		r.Log.V(3).Info("access key Secret was up-to-date, nothing done")
	}

	return nil
}

func getExpectedSecretContent(format v1alpha1.Format, id, secret string) map[string][]byte {
	switch format {
	case v1alpha1.AWSConfigFormat:
		v := fmt.Sprintf(`
[default]
aws_access_key_id=%s
aws_secret_access_key=%s
`, id, secret)

		return map[string][]byte{
			"config": []byte(v),
		}
	default:
		return map[string][]byte{
			"AWS_ACCESS_KEY_ID":     []byte(id),
			"AWS_SECRET_ACCESS_KEY": []byte(secret),
		}
	}
}

func (r *KeyReconciler) reconcileDeletion(_ context.Context, key *v1alpha1.GarageAccessKey) error {
	if key.Spec.ReclaimPolicy == v1alpha1.Retain {
		r.Log.Info("reclaim policy set to Retain, skipping target key deletion")
		return nil
	}

	keyName := key.TargetName()
	keyInfo, resp, err := r.GarageManager.getKeyByID(keyName, false)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusBadRequest {
			r.Log.Info("target key not found, skipping deletion")
			return nil
		}
		return fmt.Errorf("unable to get target key info: %w", err)
	}

	resp, err = r.GarageManager.deleteKey(keyInfo.AccessKeyId)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("unable to delete target key %s: %w", resp.Status, err)
		}
		return fmt.Errorf("unable to delete target key: %w", err)
	}

	r.Log.Info("target key deleted")

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *KeyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.GarageAccessKey{}).
		Named("key").
		Complete(r)
}
