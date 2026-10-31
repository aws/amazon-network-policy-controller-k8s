//go:build e2e

/*
Copyright Amazon.com Inc. or its affiliates. All Rights Reserved.

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

package startup

import (
	"context"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// transientBackoff retries for about two minutes, long enough for etcd to catch up after a scenario's cleanup.
var transientBackoff = wait.Backoff{Duration: time.Second, Factor: 2, Jitter: 0.1, Steps: 9, Cap: 30 * time.Second}

// retryingClient retries requests that the API server rejects with a transient error. The suite creates and
// deletes tens of thousands of objects, and while etcd catches up on those writes the API server rejects requests
// with "etcdserver: throttle: too many requests" (429). client-go only retries 429s that carry a Retry-After header,
// which these do not.
//
// Retrying is safe for every caller: creates ignore AlreadyExists, deletes ignore NotFound, and patches are merge
// patches computed against the object the caller read.
type retryingClient struct {
	client.Client
}

func isTransient(err error) bool {
	return apierrors.IsTooManyRequests(err) || apierrors.IsServerTimeout(err) || apierrors.IsTimeout(err) ||
		apierrors.IsServiceUnavailable(err)
}

func withRetry(fn func() error) error {
	return retry.OnError(transientBackoff, isTransient, fn)
}

func (c retryingClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return withRetry(func() error { return c.Client.Get(ctx, key, obj, opts...) })
}

func (c retryingClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return withRetry(func() error { return c.Client.List(ctx, list, opts...) })
}

func (c retryingClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return withRetry(func() error { return c.Client.Create(ctx, obj, opts...) })
}

func (c retryingClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return withRetry(func() error { return c.Client.Delete(ctx, obj, opts...) })
}

func (c retryingClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return withRetry(func() error { return c.Client.DeleteAllOf(ctx, obj, opts...) })
}

func (c retryingClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	return withRetry(func() error { return c.Client.Update(ctx, obj, opts...) })
}

func (c retryingClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return withRetry(func() error { return c.Client.Patch(ctx, obj, patch, opts...) })
}
