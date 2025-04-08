/*
Copyright 2024 The Kubernetes Authors.

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

package gentype2

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	rest "k8s.io/client-go/rest"
	"k8s.io/client-go/util/apply"
	"k8s.io/client-go/util/consistencydetector"
	"k8s.io/client-go/util/watchlist"
	"k8s.io/klog/v2"
)

// runtimeObject matches a runtime.Object where the target type is known.
type runtimeObject[T any] interface {
	*T
	runtime.Object
}

// objectWithMeta matches objects implementing both runtime.Object and metav1.Object.
type objectWithMeta[T any] interface {
	*T
	runtimeObject[T]
	metav1.Object
}

// namedObject matches comparable objects implementing GetName(); it is intended for use with apply declarative configurations.
type namedObject interface {
	comparable
	GetName() *string
}

// namedRuntimeObject matches a runtime.Object implementing GetName().
type namedRuntimeObject[T any] interface {
	*T
	runtimeObject[T]
	GetName() string
}

// UntypedClient represents an untyped client, optionally namespaced.
type UntypedClient struct {
	resource       string
	client         rest.Interface
	namespace      string // "" for non-namespaced clients
	parameterCodec runtime.ParameterCodec

	prefersProtobuf bool
}

// Client represents a client, optionally namespaced, with no support for lists or apply declarative configurations.
type Client[PT objectWithMeta[T], T any] struct {
	UntypedClient
}

// ClientWithList represents a client with support for lists.
type ClientWithList[PT objectWithMeta[T], PL runtimeObject[L], T any, L any] struct {
	*Client[PT, T]
	alsoLister[PT, PL, T, L]
}

// ClientWithApply represents a client with support for apply declarative configurations.
type ClientWithApply[PT objectWithMeta[T], C namedObject, T any] struct {
	*Client[PT, T]
	alsoApplier[PT, C, T]
}

// ClientWithListAndApply represents a client with support for lists and apply declarative configurations.
type ClientWithListAndApply[PT objectWithMeta[T], PL runtimeObject[L], C namedObject, T any, L any] struct {
	*Client[PT, T]
	alsoLister[PT, PL, T, L]
	alsoApplier[PT, C, T]
}

// Helper types for composition
type alsoLister[PT objectWithMeta[T], PL runtimeObject[L], T any, L any] struct {
	client *Client[PT, T]
}

type alsoApplier[PT objectWithMeta[T], C namedObject, T any] struct {
	client *Client[PT, T]
}

type Option func(*UntypedClient)

func PrefersProtobuf() Option {
	return func(c *UntypedClient) { c.prefersProtobuf = true }
}

// NewClient constructs a client, namespaced or not, with no support for lists or apply.
// Non-namespaced clients are constructed by passing an empty namespace ("").
func NewClient[PT objectWithMeta[T], T any](
	resource string, client rest.Interface, parameterCodec runtime.ParameterCodec, namespace string,
	options ...Option,
) *Client[PT, T] {
	c := &Client[PT, T]{
		UntypedClient{
			resource:       resource,
			client:         client,
			parameterCodec: parameterCodec,
			namespace:      namespace,
		},
	}
	for _, option := range options {
		option(&c.UntypedClient)
	}
	return c
}

// NewClientWithList constructs a namespaced client with support for lists.
func NewClientWithList[PT objectWithMeta[T], PL runtimeObject[L], T any, L any](
	resource string, client rest.Interface, parameterCodec runtime.ParameterCodec, namespace string,
	options ...Option,
) *ClientWithList[PT, PL, T, L] {
	typeClient := NewClient[PT](resource, client, parameterCodec, namespace, options...)
	return &ClientWithList[PT, PL, T, L]{
		typeClient,
		alsoLister[PT, PL, T, L]{typeClient},
	}
}

// NewClientWithApply constructs a namespaced client with support for apply declarative configurations.
func NewClientWithApply[PT objectWithMeta[T], C namedObject, T any](
	resource string, client rest.Interface, parameterCodec runtime.ParameterCodec, namespace string,
	options ...Option,
) *ClientWithApply[PT, C, T] {
	typeClient := NewClient[PT](resource, client, parameterCodec, namespace, options...)
	return &ClientWithApply[PT, C, T]{
		typeClient,
		alsoApplier[PT, C, T]{typeClient},
	}
}

// NewClientWithListAndApply constructs a client with support for lists and applying declarative configurations.
func NewClientWithListAndApply[PT objectWithMeta[T], PL runtimeObject[L], C namedObject, T any, L any](
	resource string, client rest.Interface, parameterCodec runtime.ParameterCodec, namespace string,
	options ...Option,
) *ClientWithListAndApply[PT, PL, C, T, L] {
	typeClient := NewClient[PT](resource, client, parameterCodec, namespace, options...)
	return &ClientWithListAndApply[PT, PL, C, T, L]{
		typeClient,
		alsoLister[PT, PL, T, L]{typeClient},
		alsoApplier[PT, C, T]{typeClient},
	}
}

// GetClient returns the REST interface.
func (c *Client[PT, T]) GetClient() rest.Interface {
	return c.client
}

// GetNamespace returns the client's namespace, if any.
func (c *Client[PT, T]) GetNamespace() string {
	return c.namespace
}

// Get takes name of the resource, and returns the corresponding object, and an error if there is any.
func (c *Client[PT, T]) Get(ctx context.Context, name string, options metav1.GetOptions) (PT, error) {
	return Get[PT](ctx, c.UntypedClient, name, options)
}

// Get takes name of a resource, and returns the corresponding object, and an error if there is any.
// R is the type of the returned resource.
func Get[PR runtimeObject[R], R any](ctx context.Context, c UntypedClient, name string, options metav1.GetOptions) (PR, error) {
	return GetSubresource[PR](ctx, c, name, options)
}

// GetSubresource takes the name of a resource and subresource, and returns the corresponding subresource object, and an error if there is any.
// S is the type of the returned subresource.
func GetSubresource[PS runtimeObject[S], S any](ctx context.Context, c UntypedClient, parentName string, options metav1.GetOptions, subresources ...string) (PS, error) {
	result := PS(new(S))
	err := c.client.Get().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		Name(parentName).
		SubResource(subresources...).
		VersionedParams(&options, c.parameterCodec).
		Do(ctx).
		Into(result)
	return result, err
}

// List takes label and field selectors, and returns the list of resources that match those selectors.
func (l *alsoLister[PT, PL, T, L]) List(ctx context.Context, opts metav1.ListOptions) (PL, error) {
	return List[PL](ctx, l.client.UntypedClient, opts)
}

// List takes label and field selectors, and returns the list of resources that match those selectors.
func List[PL runtimeObject[L], L any](ctx context.Context, c UntypedClient, opts metav1.ListOptions) (PL, error) {
	listFunc := func(ctx context.Context, opts metav1.ListOptions) (PL, error) {
		return list[PL](ctx, c, opts)
	}
	if watchListOptions, hasWatchListOptionsPrepared, watchListOptionsErr := watchlist.PrepareWatchListOptionsFromListOptions(opts); watchListOptionsErr != nil {
		klog.Warningf("Failed preparing watchlist options for $.type|resource$, falling back to the standard LIST semantics, err = %v", watchListOptionsErr)
	} else if hasWatchListOptionsPrepared {
		result, err := watchList[PL](ctx, c, watchListOptions)
		if err == nil {
			consistencydetector.CheckWatchListFromCacheDataConsistencyIfRequested(ctx, "watchlist request for "+c.resource, listFunc, opts, result)
			return result, nil
		}
		klog.Warningf("The watchlist request for %s ended with an error, falling back to the standard LIST semantics, err = %v", c.resource, err)
	}
	result, err := list[PL](ctx, c, opts)
	if err == nil {
		consistencydetector.CheckListFromCacheDataConsistencyIfRequested(ctx, "list request for "+c.resource, listFunc, opts, result)
	}
	return result, err
}

func list[PL runtimeObject[L], L any](ctx context.Context, c UntypedClient, opts metav1.ListOptions) (PL, error) {
	list := PL(new(L))
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	err := c.client.Get().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		VersionedParams(&opts, c.parameterCodec).
		Timeout(timeout).
		Do(ctx).
		Into(list)
	return list, err
}

// watchList establishes a watch stream with the server and returns the list of resources.
func watchList[PL runtimeObject[L], L any](ctx context.Context, c UntypedClient, opts metav1.ListOptions) (PL, error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	result := PL(new(L))
	err := c.client.Get().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		VersionedParams(&opts, c.parameterCodec).
		Timeout(timeout).
		WatchList(ctx).
		Into(result)
	return result, err
}

// ListSubresource takes label and field selectors, and returns the list of subresources that match those selectors.
func ListSubresource[PL runtimeObject[L], L any](ctx context.Context, c UntypedClient, parentName string, opts metav1.ListOptions, subresources ...string) (PL, error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	result := PL(new(L))
	err := c.client.Get().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		Name(parentName).
		SubResource(subresources...).
		VersionedParams(&opts, c.parameterCodec).
		Timeout(timeout).
		Do(ctx).
		Into(result)
	return result, err
}

// Watch returns a watch.Interface that watches the requested resources.
func (c *Client[PT, T]) Watch(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	opts.Watch = true
	return c.client.Get().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		VersionedParams(&opts, c.parameterCodec).
		Timeout(timeout).
		Watch(ctx)
}

// Create takes the representation of a resource and creates it.  Returns the server's representation of the resource, and an error, if there is any.
func (c *Client[PT, T]) Create(ctx context.Context, obj PT, opts metav1.CreateOptions) (PT, error) {
	return Create[PT](ctx, c.UntypedClient, obj, opts)
}

// Create takes the representation of a resource and creates it.  Returns the server's representation of the resource, and an error, if there is any.
// PI is the input pointer type, R is the type of the returned resource.
func Create[PR runtimeObject[R], R any, PI runtime.Object](ctx context.Context, c UntypedClient, obj PI, opts metav1.CreateOptions) (PR, error) {
	result := PR(new(R))
	err := c.client.Post().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		VersionedParams(&opts, c.parameterCodec).
		Body(obj).
		Do(ctx).
		Into(result)
	return result, err
}

// CreateSubresource takes the representation of a subresource and creates it in the resource with the given name.
// Returns the server's representation of the subresource, and an error, if there is any.
// PI is the input pointer type (for the subresource), S is the type of the returned subresource.
func CreateSubresource[PS runtimeObject[S], S any, PI runtime.Object](ctx context.Context, c UntypedClient, parentName string, obj PI, opts metav1.CreateOptions, subresources ...string) (PS, error) {
	result := PS(new(S))
	err := c.client.Post().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		Name(parentName).
		SubResource(subresources...).
		VersionedParams(&opts, c.parameterCodec).
		Body(obj).
		Do(ctx).
		Into(result)
	return result, err
}

// Update takes the representation of a resource and updates it. Returns the server's representation of the resource, and an error, if there is any.
func (c *Client[PT, T]) Update(ctx context.Context, obj PT, opts metav1.UpdateOptions) (PT, error) {
	return Update[PT](ctx, c.UntypedClient, obj, opts)
}

// Update takes the representation of a resource and updates it. Returns the server's representation of the resource, and an error, if there is any.
// I is the input subresource type, PI the corresponding pointer type; R is the type of the returned subresource and PR the corresponding pointer type.
func Update[PR runtimeObject[R], R any, PI namedRuntimeObject[I], I any](ctx context.Context, c UntypedClient, obj PI, opts metav1.UpdateOptions) (PR, error) {
	return UpdateSubresource[PR](ctx, c, obj.GetName(), obj, opts)
}

// UpdateSubresource takes the representation of a subresource and updates it. Returns the server's representation of the subresource, and an error, if there is any.
// I is the input subresource type, PI the corresponding pointer type; S is the type of the returned subresource and PS the corresponding pointer type.
func UpdateSubresource[PS runtimeObject[S], S any, PI runtimeObject[I], I any](ctx context.Context, c UntypedClient, parentName string, obj PI, opts metav1.UpdateOptions, subresources ...string) (PS, error) {
	result := PS(new(S))
	err := c.client.Put().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		Name(parentName).
		SubResource(subresources...).
		VersionedParams(&opts, c.parameterCodec).
		Body(obj).
		Do(ctx).
		Into(result)
	return result, err
}

// UpdateStatus updates the status subresource of a resource. Returns the server's representation of the resource, and an error, if there is any.
func (c *Client[PT, T]) UpdateStatus(ctx context.Context, obj PT, opts metav1.UpdateOptions) (PT, error) {
	return UpdateStatus(ctx, c.UntypedClient, obj, opts)
}

// UpdateStatus updates the status subresource of a resource. Returns the server's representation of the resource, and an error, if there is any.
// This is provided as a separate function because, even though it manipulates a subresource just like UpdateSubresource,
// it returns the parent resource rather than the updated subresource (this is tied to the Kubernetes REST API).
func UpdateStatus[PR namedRuntimeObject[R], R any](ctx context.Context, c UntypedClient, obj PR, opts metav1.UpdateOptions) (PR, error) {
	return UpdateSubresource[PR](ctx, c, obj.GetName(), obj, opts, "status")
}

// Delete takes name of the resource and deletes it. Returns an error if one occurs.
func (c *Client[PT, T]) Delete(ctx context.Context, name string, opts metav1.DeleteOptions) error {
	return Delete(ctx, c.UntypedClient, name, opts)
}

// Delete takes name of the resource and deletes it. Returns an error if one occurs.
func Delete(ctx context.Context, c UntypedClient, name string, opts metav1.DeleteOptions) error {
	return c.client.Delete().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		Name(name).
		Body(&opts).
		Do(ctx).
		Error()
}

// DeleteCollection deletes a collection of objects.
func (l *alsoLister[PT, PL, T, L]) DeleteCollection(ctx context.Context, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	return DeleteCollection(ctx, l.client.UntypedClient, opts, listOpts)
}

// DeleteCollection deletes a collection of objects.
func DeleteCollection(ctx context.Context, c UntypedClient, opts metav1.DeleteOptions, listOpts metav1.ListOptions) error {
	var timeout time.Duration
	if listOpts.TimeoutSeconds != nil {
		timeout = time.Duration(*listOpts.TimeoutSeconds) * time.Second
	}
	return c.client.Delete().
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		VersionedParams(&listOpts, c.parameterCodec).
		Timeout(timeout).
		Body(&opts).
		Do(ctx).
		Error()
}

// Patch applies the patch and returns the patched resource.
func (c *Client[PT, T]) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (PT, error) {
	return Patch[PT](ctx, c.UntypedClient, name, pt, data, opts, subresources...)
}

// Patch applies the patch and returns the patched resource.
func Patch[PR runtimeObject[R], R any](ctx context.Context, c UntypedClient, name string, pt types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (PR, error) {
	result := PR(new(R))
	err := c.client.Patch(pt).
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		Name(name).
		SubResource(subresources...).
		VersionedParams(&opts, c.parameterCodec).
		Body(data).
		Do(ctx).
		Into(result)
	return result, err
}

// Apply takes the given apply declarative configuration, applies it and returns the applied resource.
func (a *alsoApplier[PT, C, T]) Apply(ctx context.Context, obj C, opts metav1.ApplyOptions) (PT, error) {
	return Apply[PT](ctx, a.client.UntypedClient, obj, opts)
}

// Apply takes the given apply declarative configuration, applies it and returns the applied resource.
func Apply[PR objectWithMeta[R], C namedObject, R any](ctx context.Context, c UntypedClient, obj C, opts metav1.ApplyOptions) (PR, error) {
	if obj == *new(C) {
		return new(R), fmt.Errorf("object provided to Apply must not be nil")
	}
	if obj.GetName() == nil {
		return new(R), fmt.Errorf("obj.Name must be provided to Apply")
	}
	return ApplySubresource[PR](ctx, c, *obj.GetName(), obj, opts)
}

// ApplySubresource takes the given apply declarative configuration, applies it to the subresource, and returns the applied subresource
// (or resource for status subresources).
func ApplySubresource[PR runtimeObject[R], C comparable, R any](ctx context.Context, c UntypedClient, parentName string, obj C, opts metav1.ApplyOptions, subresources ...string) (PR, error) {
	result := PR(new(R))
	if obj == *new(C) {
		return new(R), fmt.Errorf("object provided to ApplySubresource must not be nil")
	}
	patchOpts := opts.ToPatchOptions()

	request, err := apply.NewRequest(c.client, obj)
	if err != nil {
		return new(R), err
	}

	err = request.
		UseProtobufAsDefaultIfPreferred(c.prefersProtobuf).
		NamespaceIfScoped(c.namespace, c.namespace != "").
		Resource(c.resource).
		Name(parentName).
		SubResource(subresources...).
		VersionedParams(&patchOpts, c.parameterCodec).
		Do(ctx).
		Into(result)
	return result, err
}

// ApplyStatus takes the given apply declarative configuration, applies it to the status subresource and returns the applied resource.
func (a *alsoApplier[PT, C, T]) ApplyStatus(ctx context.Context, obj C, opts metav1.ApplyOptions) (PT, error) {
	return ApplyStatus[PT](ctx, a.client.UntypedClient, obj, opts)
}

// ApplyStatus takes the given apply declarative configuration, applies it to the status subresource and returns the applied resource.
func ApplyStatus[PR objectWithMeta[R], C namedObject, R any](ctx context.Context, c UntypedClient, obj C, opts metav1.ApplyOptions) (PR, error) {
	if obj == *new(C) {
		return new(R), fmt.Errorf("object provided to ApplyStatus must not be nil")
	}
	if obj.GetName() == nil {
		return new(R), fmt.Errorf("obj.Name must be provided to ApplyStatus")
	}
	return ApplySubresource[PR](ctx, c, *obj.GetName(), obj, opts, "status")
}
