package controller

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"time"

	garage "git.deuxfleurs.fr/garage-sdk/garage-admin-sdk-golang"
	. "github.com/onsi/gomega"

	garagev1alpha1 "github.com/spiarh/garage-s3-operator/api/v1alpha1"
)

type garageMockRoute struct {
	method string
	path   string
}

type garageResponder func(http.ResponseWriter, *http.Request, *GarageMockServer)

type fakeGarageBucket struct {
	ID         string
	Alias      string
	MaxObjects int64
	MaxSize    int64
}

type fakeGarageKey struct {
	ID            string
	Name          string
	SecretKey     string
	Bucket        *fakeGarageBucket
	Permissions   *garagev1alpha1.Permissions
	ShowSecretKey bool
}

type GarageMockServer struct {
	server *httptest.Server

	mu sync.Mutex

	routes    map[garageMockRoute][]garageResponder
	fallbacks map[garageMockRoute]garageResponder
	calls     map[garageMockRoute]int

	lastAuthHeader string
}

func NewGarageMockServer() *GarageMockServer {
	mock := &GarageMockServer{
		routes:    map[garageMockRoute][]garageResponder{},
		fallbacks: map[garageMockRoute]garageResponder{},
		calls:     map[garageMockRoute]int{},
	}

	mock.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mock.mu.Lock()
		defer mock.mu.Unlock()

		mock.lastAuthHeader = r.Header.Get("Authorization")
		route := garageMockRoute{method: r.Method, path: r.URL.Path}
		mock.calls[route]++

		if queued := mock.routes[route]; len(queued) > 0 {
			responder := queued[0]
			mock.routes[route] = queued[1:]
			responder(w, r, mock)
			return
		}

		if fallback, ok := mock.fallbacks[route]; ok {
			fallback(w, r, mock)
			return
		}

		http.Error(w, fmt.Sprintf("no queued responder for %s %s", r.Method, r.URL.String()), http.StatusInternalServerError)
	}))

	return mock
}

func (m *GarageMockServer) URL() string {
	return m.server.URL
}

func (m *GarageMockServer) Close() {
	m.server.Close()
}

func (m *GarageMockServer) QueueResponder(method string, path string, responder garageResponder) {
	m.mu.Lock()
	defer m.mu.Unlock()

	route := garageMockRoute{method: method, path: path}
	m.routes[route] = append(m.routes[route], responder)
}

func (m *GarageMockServer) SetFallbackResponder(method string, path string, responder garageResponder) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.fallbacks[garageMockRoute{method: method, path: path}] = responder
}

func (m *GarageMockServer) RequestCount(method string, path string) int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.calls[garageMockRoute{method: method, path: path}]
}

func (m *GarageMockServer) LastAuthHeader() string {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.lastAuthHeader
}

func respondNotFound(w http.ResponseWriter, r *http.Request, _ *GarageMockServer) {
	http.NotFound(w, r)
}

func typedBucketResponse(bucket fakeGarageBucket) garage.GetBucketInfoResponse {
	quotas := garage.NewApiBucketQuotas()
	quotas.SetMaxObjects(bucket.MaxObjects)
	quotas.SetMaxSize(bucket.MaxSize)

	return garage.GetBucketInfoResponse{
		Bytes:                          0,
		Created:                        time.Date(2026, time.March, 12, 0, 0, 0, 0, time.UTC),
		GlobalAliases:                  []string{bucket.Alias},
		Id:                             bucket.ID,
		Keys:                           []garage.GetBucketInfoKey{},
		Objects:                        0,
		Quotas:                         *quotas,
		UnfinishedMultipartUploadBytes: 0,
		UnfinishedMultipartUploadParts: 0,
		UnfinishedMultipartUploads:     0,
		UnfinishedUploads:              0,
		WebsiteAccess:                  false,
	}
}

func typedBucketListItem(bucket fakeGarageBucket) garage.ListBucketsResponseItem {
	return garage.ListBucketsResponseItem{
		Created:       time.Date(2026, time.March, 12, 0, 0, 0, 0, time.UTC),
		GlobalAliases: []string{bucket.Alias},
		Id:            bucket.ID,
		LocalAliases:  []garage.BucketLocalAlias{},
	}
}

func typedBucketPerms(permissions *garagev1alpha1.Permissions) garage.ApiBucketKeyPerm {
	bucketPerms := garage.NewApiBucketKeyPerm()
	if permissions != nil {
		bucketPerms.SetOwner(permissions.Owner)
		bucketPerms.SetRead(permissions.Read)
		bucketPerms.SetWrite(permissions.Write)
	}
	return *bucketPerms
}

func typedKeyPermissions() garage.KeyPerm {
	keyPerms := garage.NewKeyPerm()
	keyPerms.SetCreateBucket(false)
	return *keyPerms
}

func typedKeyResponse(key fakeGarageKey) garage.GetKeyInfoResponse {
	response := garage.GetKeyInfoResponse{
		AccessKeyId: key.ID,
		Buckets:     []garage.KeyInfoBucketResponse{},
		Expired:     false,
		Name:        key.Name,
		Permissions: typedKeyPermissions(),
	}
	response.SetCreated(time.Date(2026, time.March, 12, 0, 0, 0, 0, time.UTC))

	if key.Bucket != nil {
		response.Buckets = append(response.Buckets, garage.KeyInfoBucketResponse{
			GlobalAliases: []string{key.Bucket.Alias},
			Id:            key.Bucket.ID,
			LocalAliases:  []string{},
			Permissions:   typedBucketPerms(key.Permissions),
		})
	}

	if key.ShowSecretKey {
		response.SetSecretAccessKey(key.SecretKey)
	}

	return response
}

func typedKeyListItem(key fakeGarageKey) garage.ListKeysResponseItem {
	item := garage.ListKeysResponseItem{
		Expired: false,
		Id:      key.ID,
		Name:    key.Name,
	}
	item.SetCreated(time.Date(2026, time.March, 12, 0, 0, 0, 0, time.UTC))
	return item
}

func respondWithBucket(bucket fakeGarageBucket) garageResponder {
	return func(w http.ResponseWriter, _ *http.Request, _ *GarageMockServer) {
		writeJSON(w, http.StatusOK, typedBucketResponse(bucket))
	}
}

//nolint:all
func respondWithBucketList(buckets ...fakeGarageBucket) garageResponder {
	return func(w http.ResponseWriter, _ *http.Request, _ *GarageMockServer) {
		payload := make([]garage.ListBucketsResponseItem, 0, len(buckets))
		for _, bucket := range buckets {
			payload = append(payload, typedBucketListItem(bucket))
		}
		writeJSON(w, http.StatusOK, payload)
	}
}

func respondWithEmptyKeyList(w http.ResponseWriter, _ *http.Request, _ *GarageMockServer) {
	writeJSON(w, http.StatusOK, []garage.ListKeysResponseItem{})
}

func respondWithKeyList(keys ...fakeGarageKey) garageResponder {
	return func(w http.ResponseWriter, _ *http.Request, _ *GarageMockServer) {
		payload := make([]garage.ListKeysResponseItem, 0, len(keys))
		for _, key := range keys {
			payload = append(payload, typedKeyListItem(key))
		}
		writeJSON(w, http.StatusOK, payload)
	}
}

func respondWithCreatedKeyFromBody(bucket *fakeGarageBucket, permissions *garagev1alpha1.Permissions) garageResponder {
	return func(w http.ResponseWriter, r *http.Request, _ *GarageMockServer) {
		body, err := io.ReadAll(r.Body)
		Expect(err).NotTo(HaveOccurred())

		payload := garage.NewUpdateKeyRequestBody()
		Expect(json.Unmarshal(body, payload)).To(Succeed())
		name, ok := payload.GetNameOk()
		Expect(ok).To(BeTrue())
		Expect(name).NotTo(BeNil())
		Expect(*name).NotTo(BeEmpty())

		writeJSON(w, http.StatusOK, typedKeyResponse(fakeGarageKey{
			ID:          "key-" + *name,
			Name:        *name,
			SecretKey:   "secret-" + *name,
			Bucket:      bucket,
			Permissions: permissions,
		}))
	}
}

func respondWithKeyInfoForBucket(bucket *fakeGarageBucket, permissions *garagev1alpha1.Permissions) garageResponder {
	return func(w http.ResponseWriter, r *http.Request, _ *GarageMockServer) {
		search := r.URL.Query().Get("search")
		Expect(search).NotTo(BeEmpty())

		writeJSON(w, http.StatusOK, typedKeyResponse(fakeGarageKey{
			ID:          "key-" + search,
			Name:        search,
			SecretKey:   "secret-" + search,
			Bucket:      bucket,
			Permissions: permissions,
			//nolint:goconst
			ShowSecretKey: r.URL.Query().Get("showSecretKey") == "true",
		}))
	}
}

func respondWithBucketPermissionUpdate(bucket fakeGarageBucket) garageResponder {
	return func(w http.ResponseWriter, _ *http.Request, _ *GarageMockServer) {
		writeJSON(w, http.StatusOK, typedBucketResponse(bucket))
	}
}

//nolint:unparam
func writeJSON(w http.ResponseWriter, statusCode int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	Expect(json.NewEncoder(w).Encode(payload)).To(Succeed())
}
