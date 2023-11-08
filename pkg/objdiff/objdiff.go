package objdiff

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/google/go-cmp/cmp"
	kerrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/json"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/restmapper"
	"k8s.io/utils/strings/slices"
)

type Object struct {
	v1.TypeMeta   `json:",inline"`
	v1.ObjectMeta `json:"metadata"`
	Spec          any       `json:"spec,omitempty"`
	Data          any       `json:"data,omitempty"`
	Items         []*Object `json:"items,omitempty"`
}

func (o *Object) IsList() bool {
	return o.Items != nil
}

func (o *Object) String() string {
	if o.Namespace == "" {
		return fmt.Sprintf("%s %s %s", o.APIVersion, o.Kind, o.Name)
	}
	return fmt.Sprintf("%s %s %s/%s", o.APIVersion, o.Kind, o.Namespace, o.Name)
}

type Differ interface {
	Diff(apiVersion, kind string, obj *Object, opts ...cmp.Option) ([]string, []string, error)
}

type Diff struct {
	client dynamic.Interface
	mapper meta.RESTMapper
}

func New(config *rest.Config) (*Diff, error) {
	mapper, err := getRESTMapper(config)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	client, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, errors.WithStack(err)
	}

	return &Diff{client: client, mapper: mapper}, nil
}

func (d *Diff) Diff(apiVersion, kind string, obj *Object, opts ...cmp.Option) ([]string, []string, error) {
	resource, err := d.getResource(apiVersion, kind)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}

	if obj.IsList() {
		return d.diffList(resource, obj, opts...)
	}
	return d.diffObj(resource, obj, opts...)
}

func (d *Diff) diffObj(resource schema.GroupVersionResource, obj *Object, opts ...cmp.Option) ([]string, []string, error) {
	remote, err := d.getRemoteObj(resource, obj)
	if kerrors.IsNotFound(errors.Cause(err)) {
		return []string{fmt.Sprintf("- %s is not found\n", obj)}, []string{}, nil
	}
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	diff := DiffObj(obj, remote, opts...)
	if diff != "" {
		return []string{}, []string{diff}, nil
	}
	return []string{}, []string{}, nil
}

func (d *Diff) diffList(resource schema.GroupVersionResource, obj *Object, opts ...cmp.Option) ([]string, []string, error) {
	remote, err := d.getRemoteObjs(resource)
	if err != nil {
		return nil, nil, errors.WithStack(err)
	}
	presences, diffs := DiffList(obj.Items, remote, opts...)
	return presences, diffs, nil
}

func (d *Diff) getRemoteObjs(resource schema.GroupVersionResource) ([]*Object, error) {
	resp, err := d.client.
		Resource(resource).
		List(context.Background(), v1.ListOptions{})
	if err != nil {
		return nil, errors.WithStack(err)
	}

	out := make([]*Object, 0)
	for _, i := range resp.Items {
		newObj := new(Object)
		err = unmarshallUnstructured(&i, newObj)
		if err != nil {
			return nil, errors.WithStack(err)
		}
		out = append(out, newObj)
	}
	return out, nil
}

func (d *Diff) getRemoteObj(resource schema.GroupVersionResource, obj *Object) (*Object, error) {
	var resp *unstructured.Unstructured
	var err error
	if obj.Namespace != "" {
		resp, err = d.client.
			Resource(resource).
			Namespace(obj.Namespace).
			Get(context.Background(), obj.Name, v1.GetOptions{})
	} else {
		resp, err = d.client.
			Resource(resource).
			Get(context.Background(), obj.Name, v1.GetOptions{})
	}
	if err != nil {
		return nil, errors.WithStack(err)
	}

	newObj := new(Object)
	err = unmarshallUnstructured(resp, newObj)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	return newObj, nil
}

func (d *Diff) getResource(apiVersion, kind string) (schema.GroupVersionResource, error) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil {
		return schema.GroupVersionResource{}, errors.WithStack(err)
	}

	gvk := gv.WithKind(kind)
	mapping, err := d.mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
	if err != nil {
		return schema.GroupVersionResource{}, errors.WithStack(err)
	}
	return mapping.Resource, nil
}

func IgnoreMapEntries(ignoredKeys []string) cmp.Option {
	filter := func(path cmp.Path) bool {
		var key []string
		for _, ps := range path {
			switch x := ps.(type) {
			case cmp.MapIndex:
				key = append(key, x.Key().String())
			case cmp.SliceIndex:
				key = append(key, strconv.Itoa(x.Key()))
			}
		}
		// check it naively since ignoredKeys won't be so long,
		return slices.Contains(ignoredKeys, strings.Join(key, "."))
	}
	return cmp.FilterPath(filter, cmp.Ignore())
}

func getRESTMapper(config *rest.Config) (meta.RESTMapper, error) {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	groupResources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		return nil, errors.WithStack(err)
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)
	return mapper, nil
}

func unmarshallUnstructured(u *unstructured.Unstructured, v any) error {
	rawJson, err := u.MarshalJSON()
	if err != nil {
		return errors.WithStack(err)
	}
	return json.Unmarshal(rawJson, v)
}

func DiffObj(obj1, obj2 *Object, opts ...cmp.Option) string {
	if obj1.Kind == "ConfigMap" {
		return cmp.Diff(obj1.Data, obj2.Data, opts...)
	}
	return cmp.Diff(obj1.Spec, obj2.Spec, opts...)
}

func DiffList(obj1, obj2 []*Object, opts ...cmp.Option) (presences, diffs []string) {
	m := make(map[string]*Object)
	checked := make(map[string]bool)
	for _, o := range obj1 {
		m[o.String()] = o
		checked[o.String()] = false
	}
	for _, o2 := range obj2 {
		o1, ok := m[o2.String()]
		if !ok {
			presences = append(presences, fmt.Sprintf("- %s is not found\n", o2))
			continue
		}
		checked[o2.String()] = true
		diff := DiffObj(o1, o2, opts...)
		if diff == "" {
			continue
		}
		diffs = append(diffs, fmt.Sprintf("%s\n%s", o2.String(), diff))
	}
	for k, v := range m {
		if !checked[k] {
			presences = append(presences, fmt.Sprintf("+ %s is found, but not in default\n", v))
		}
	}
	return presences, diffs
}
