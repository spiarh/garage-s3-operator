package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/spiarh/garage-s3-operator/api/v1alpha1"
)

type NamespaceDeniedError struct {
	namespace             string
	clusterNamespacedName types.NamespacedName
}

func (e *NamespaceDeniedError) Error() string {
	return fmt.Sprintf("Namespace %s ist not allowed to consume Cluster %s", e.namespace, e.clusterNamespacedName.String())
}

func getBucket(ctx context.Context, k8sClient client.Client, s *v1alpha1.Selector) (*v1alpha1.GarageBucket, error) {
	if s == nil {
		return nil, fmt.Errorf("bucket selector is required")
	}

	bucket := &v1alpha1.GarageBucket{}
	err := k8sClient.Get(ctx, s.NamespacedName(), bucket)
	if err != nil {
		return nil, err
	}
	return bucket, nil
}

func getKey(ctx context.Context, k8sClient client.Client, s *v1alpha1.Selector) (*v1alpha1.GarageAccessKey, error) {
	if s == nil {
		return nil, fmt.Errorf("key selector is required")
	}

	key := &v1alpha1.GarageAccessKey{}
	err := k8sClient.Get(ctx, s.NamespacedName(), key)
	if err != nil {
		return nil, err
	}
	return key, nil
}

func getCluster(ctx context.Context, k8sClient client.Client, s *v1alpha1.Selector) (*v1alpha1.GarageCluster, error) {
	if s == nil {
		return nil, fmt.Errorf("cluster selector is required")
	}

	cluster := &v1alpha1.GarageCluster{}
	err := k8sClient.Get(ctx, s.NamespacedName(), cluster)
	if err != nil {
		return nil, err
	}

	return cluster, nil
}

func getSecretValue(ctx context.Context, k8sClient client.Client, ns string, s *corev1.SecretKeySelector) (string, error) {
	secret := &corev1.Secret{}
	req := types.NamespacedName{
		Name:      s.Name,
		Namespace: ns,
	}
	err := k8sClient.Get(ctx, req, secret)
	if err != nil {
		return "", err
	}

	if data, ok := secret.Data[s.Key]; ok {
		return string(data), nil
	}

	return "", fmt.Errorf("key %s not found in secret %s", s.Key, s.Name)
}
