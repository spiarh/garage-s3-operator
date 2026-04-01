package controller

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"time"

	garage "git.deuxfleurs.fr/garage-sdk/garage-admin-sdk-golang"
	"github.com/go-logr/logr"
	"github.com/spiarh/garage-s3-operator/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type GarageAPI interface {
	getBucket(name string) (*garage.GetBucketInfoResponse, *http.Response, error)
	createBucket(name string) (*garage.GetBucketInfoResponse, *http.Response, error)
	updateBucket(id string, quotas *v1alpha1.BucketQuotas) (*garage.GetBucketInfoResponse, *http.Response, error)
	deleteBucket(id string) (*http.Response, error)
	getKeyByID(id string, showSecretKey bool) (*garage.GetKeyInfoResponse, *http.Response, error)
	createKey(name string) (*garage.GetKeyInfoResponse, *http.Response, error)
	deleteKey(id string) (*http.Response, error)
	listKeys() ([]garage.ListKeysResponseItem, *http.Response, error)
	findKeyInList(name string) (*garage.ListKeysResponseItem, error)
	findBucketInList(name string) (*garage.ListBucketsResponseItem, error)
	setPermissions(accessKeyId, bucketID string, keyPerm *v1alpha1.Permissions) (*garage.GetBucketInfoResponse, *http.Response, error)
	denyAllPermissions(accessKeyId, bucketID string) (*http.Response, error)
}

type GarageManagerFactory func(context.Context, client.Client, logr.Logger, v1alpha1.GarageCluster) (GarageAPI, error)

type GarageManager struct {
	Log    logr.Logger
	Client *garage.APIClient
	Ctx    context.Context
}

type loggingRoundTripper struct {
	base http.RoundTripper
	log  logr.Logger
}

func defaultGarageManagerFactory(k8sCtx context.Context, k8sClient client.Client, log logr.Logger, cluster v1alpha1.GarageCluster) (GarageAPI, error) {
	return newGarageManager(k8sCtx, k8sClient, log, cluster)
}

func newGarageManager(k8sCtx context.Context, k8sClient client.Client, log logr.Logger, cluster v1alpha1.GarageCluster) (*GarageManager, error) {
	ctx, clt, err := newGarageClient(k8sCtx, k8sClient, log, cluster)
	if err != nil {
		return nil, err
	}

	return &GarageManager{
		Log:    log,
		Client: clt,
		Ctx:    ctx,
	}, nil
}

func newGarageClient(ctx context.Context, k8sClient client.Client, log logr.Logger, cluster v1alpha1.GarageCluster) (context.Context, *garage.APIClient, error) {
	parsedURL, err := url.Parse(cluster.Spec.Endpoint.URL)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to parse garage endpoint URL")
	}

	httpClient := &http.Client{}
	baseTransport := http.DefaultTransport
	if parsedURL.Scheme == "https" {
		tlsConfig := &tls.Config{
			InsecureSkipVerify: cluster.Spec.Endpoint.InsecureSkipTLSVerify,
		}

		if cluster.Spec.Endpoint.CA.SecretRef != nil {
			CA, err := getSecretValue(ctx, k8sClient, cluster.Namespace, cluster.Spec.Endpoint.CA.SecretRef)
			if err != nil {
				return nil, nil, fmt.Errorf("unable to fetch garage endpoint CA: %w", err)
			}
			certPool := x509.NewCertPool()
			if !certPool.AppendCertsFromPEM([]byte(CA)) {
				return nil, nil, fmt.Errorf("unable to parse garage endpoint CA PEM")
			}
			tlsConfig.RootCAs = certPool
		}
		baseTransport = &http.Transport{TLSClientConfig: tlsConfig}
	}
	httpClient.Transport = &loggingRoundTripper{base: baseTransport, log: log}

	config := &garage.Configuration{
		HTTPClient: httpClient,
		Servers: garage.ServerConfigurations{
			{
				URL:         cluster.Spec.Endpoint.URL,
				Description: "Target cluster",
			},
		},
	}

	adminToken, err := getSecretValue(ctx, k8sClient, cluster.Namespace, cluster.Spec.AdminTokenSecretRef)
	if err != nil {
		return nil, nil, fmt.Errorf("unable to get admin token: %w", err)
	}

	garageCtx := context.WithValue(ctx, garage.ContextAccessToken, adminToken)
	garageClient := garage.NewAPIClient(config)

	return garageCtx, garageClient, nil
}

func (rt *loggingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	resp, err := rt.base.RoundTrip(req)
	duration := time.Since(start)
	if err != nil {
		rt.log.V(10).Error(err, "garage http request failed",
			"method", req.Method,
			"url", req.URL.String(),
			"duration", duration,
		)
		return resp, err
	}

	if resp == nil {
		return nil, fmt.Errorf("HTTP response is empty")
	}

	rt.log.V(10).Info("garage http response",
		"method", req.Method,
		"url", req.URL.String(),
		"status", resp.Status,
		"statusCode", resp.StatusCode,
		"duration", duration,
		"contentLength", resp.ContentLength,
	)

	return resp, err
}

func (g *GarageManager) getBucket(name string) (*garage.GetBucketInfoResponse, *http.Response, error) {
	g.Log.V(3).Info("get target bucket info: " + name)

	req := g.Client.BucketAPI.GetBucketInfo(g.Ctx)
	bucketInfo, resp, err := req.GlobalAlias(name).Execute()
	if err != nil {
		return nil, resp, err
	}

	g.Log.V(8).Info("bucket info response", "response", bucketInfo)

	return bucketInfo, resp, nil
}

func (g *GarageManager) createBucket(name string) (*garage.GetBucketInfoResponse, *http.Response, error) {
	g.Log.Info("create new target bucket: " + name)

	createBucketReq := garage.NewCreateBucketRequest()
	createBucketReq.GlobalAlias = *garage.NewNullableString(&name)

	bucketInfo, resp, err := g.Client.BucketAPI.CreateBucket(g.Ctx).
		CreateBucketRequest(*createBucketReq).Execute()
	if err != nil {
		return nil, resp, err
	}

	g.Log.V(8).Info("bucket info response", "response", bucketInfo)

	return bucketInfo, resp, nil
}

func (g *GarageManager) updateBucket(id string, quotas *v1alpha1.BucketQuotas) (*garage.GetBucketInfoResponse, *http.Response, error) {
	g.Log.Info("update target bucket: " + id)

	q := &garage.ApiBucketQuotas{
		MaxObjects: *garage.NewNullableInt64(&quotas.MaxObjects),
		MaxSize:    *garage.NewNullableInt64(&quotas.MaxSize),
	}

	bucketInfo, resp, err := g.Client.BucketAPI.UpdateBucket(g.Ctx).
		Id(id).
		UpdateBucketRequestBody(garage.UpdateBucketRequestBody{
			Quotas: *garage.NewNullableApiBucketQuotas(q),
		}).Execute()
	if err != nil {
		return nil, resp, err
	}

	g.Log.V(8).Info("update bucket info response", "response", bucketInfo)

	return bucketInfo, resp, nil
}

func (g *GarageManager) deleteBucket(id string) (*http.Response, error) {
	g.Log.Info("delete target bucket: " + id)

	resp, err := g.Client.BucketAPI.DeleteBucket(g.Ctx).
		Id(id).Execute()
	if err != nil {
		return resp, err
	}

	return resp, nil
}

func (g *GarageManager) getKeyByID(id string, showSecretKey bool) (*garage.GetKeyInfoResponse, *http.Response, error) {
	g.Log.V(3).Info("get target key info: " + id)

	req := g.Client.AccessKeyAPI.GetKeyInfo(g.Ctx).ShowSecretKey(showSecretKey)
	keyInfo, resp, err := req.Search(id).Execute()
	if err != nil {
		return nil, resp, err
	}

	g.Log.V(8).Info("key info response", "response", keyInfo)

	return keyInfo, resp, nil
}

func (g *GarageManager) createKey(name string) (*garage.GetKeyInfoResponse, *http.Response, error) {
	g.Log.Info("create new target key: " + name)

	req := garage.NewUpdateKeyRequestBody()
	req.SetName(name)

	keyInfo, resp, err := g.Client.AccessKeyAPI.CreateKey(g.Ctx).Body(*req).Execute()
	if err != nil {
		return nil, resp, err
	}

	g.Log.V(8).Info("key info response", "response", keyInfo)

	return keyInfo, resp, nil
}

func (g *GarageManager) deleteKey(id string) (*http.Response, error) {
	g.Log.Info("delete target key: " + id)

	resp, err := g.Client.AccessKeyAPI.DeleteKey(g.Ctx).
		Id(id).Execute()
	if err != nil {
		return resp, err
	}

	return resp, nil
}

func (g *GarageManager) listKeys() ([]garage.ListKeysResponseItem, *http.Response, error) {
	g.Log.V(3).Info("list target keys")

	keys, resp, err := g.Client.AccessKeyAPI.ListKeys(g.Ctx).Execute()
	if err != nil {
		return nil, resp, err
	}

	g.Log.V(8).Info("list keys response", "response", keys)

	return keys, resp, nil
}

func (g *GarageManager) findKeyInList(name string) (*garage.ListKeysResponseItem, error) {
	g.Log.V(3).Info("find key in list: " + name)

	keys, _, err := g.listKeys()
	if err != nil {
		return nil, fmt.Errorf("unable to list keys: %w", err)
	}

	for _, key := range keys {
		if key.Name == name {
			return &key, nil
		}
	}

	return nil, nil
}

func (g *GarageManager) findBucketInList(name string) (*garage.ListBucketsResponseItem, error) {
	g.Log.V(3).Info("find bucket in list: " + name)

	buckets, _, err := g.Client.BucketAPI.ListBuckets(g.Ctx).Execute()
	if err != nil {
		return nil, fmt.Errorf("unable to list buckets: %w", err)
	}

	for _, bucket := range buckets {
		if slices.Contains(bucket.GlobalAliases, name) {
			return &bucket, nil
		}
	}

	return nil, nil
}

func (g *GarageManager) setPermissions(accessKeyId, bucketID string, keyPerm *v1alpha1.Permissions) (*garage.GetBucketInfoResponse, *http.Response, error) {
	bucketKeyPermAllow := garage.NewApiBucketKeyPerm()
	bucketKeyPermDeny := garage.NewApiBucketKeyPerm()

	if keyPerm.Owner {
		bucketKeyPermAllow.SetOwner(true)
	} else {
		bucketKeyPermDeny.SetOwner(true)
	}
	if keyPerm.Read {
		bucketKeyPermAllow.SetRead(true)
	} else {
		bucketKeyPermDeny.SetRead(true)
	}
	if keyPerm.Write {
		bucketKeyPermAllow.SetWrite(true)
	} else {
		bucketKeyPermDeny.SetWrite(true)
	}

	g.Log.Info("allow permissions on bucket: " + bucketID)
	reqAllow := garage.NewBucketKeyPermChangeRequest(accessKeyId, bucketID, *bucketKeyPermAllow)
	_, resp, err := g.Client.PermissionAPI.AllowBucketKey(g.Ctx).Body(*reqAllow).Execute()
	if err != nil {
		return nil, resp, err
	}

	g.Log.Info("deny permissions on bucket: " + bucketID)
	reqDeny := garage.NewBucketKeyPermChangeRequest(accessKeyId, bucketID, *bucketKeyPermDeny)
	bucketInfo, resp, err := g.Client.PermissionAPI.DenyBucketKey(g.Ctx).Body(*reqDeny).Execute()
	if err != nil {
		return nil, resp, err
	}

	g.Log.V(8).Info("bucket info response", "response", bucketInfo)

	return bucketInfo, resp, nil
}

func (g *GarageManager) denyAllPermissions(accessKeyId, bucketID string) (*http.Response, error) {
	g.Log.Info("deny all permissions on bucket: " + bucketID + " for key: " + accessKeyId)

	bucketKeyPerm := garage.NewApiBucketKeyPerm()
	bucketKeyPerm.SetOwner(true)
	bucketKeyPerm.SetRead(true)
	bucketKeyPerm.SetWrite(true)

	req := garage.NewBucketKeyPermChangeRequest(accessKeyId, bucketID, *bucketKeyPerm)
	_, resp, err := g.Client.PermissionAPI.DenyBucketKey(g.Ctx).Body(*req).Execute()
	if err != nil {
		return resp, err
	}

	return resp, nil
}
