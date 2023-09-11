package objdiff

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/errors"
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
	Diff(resource schema.GroupVersionResource, obj *Object, opts ...cmp.Option) ([]string, []string, error)
}

type Diff struct {
	client dynamic.Interface
}

func New(client dynamic.Interface) *Diff {
	return &Diff{client: client}
}

func (d *Diff) Diff(resource schema.GroupVersionResource, obj *Object, opts ...cmp.Option) ([]string, []string, error) {
	if obj.IsList() {
		return d.diffList(resource, obj, opts...)
	}
	return d.diffObj(resource, obj, opts...)
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

func GetRESTMapper(config *rest.Config) (meta.RESTMapper, error) {
	discoveryClient, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return nil, err
	}
	groupResources, err := restmapper.GetAPIGroupResources(discoveryClient)
	if err != nil {
		return nil, err
	}
	mapper := restmapper.NewDiscoveryRESTMapper(groupResources)
	return mapper, nil
}

func compare(a, b *Object, opts ...cmp.Option) string {
	return cmp.Diff(a.Spec, b.Spec, opts...)
}

func unmarshallUnstructured(u *unstructured.Unstructured, v any) error {
	rawJson, err := u.MarshalJSON()
	if err != nil {
		return err
	}
	return json.Unmarshal(rawJson, v)
}

func (d *Diff) diffObj(resource schema.GroupVersionResource, obj *Object, opts ...cmp.Option) (presences, diffs []string, err error) {
	// get the current manifest
	resp, err := d.client.
		Resource(resource).
		Get(context.Background(), obj.Name, v1.GetOptions{})
	if errors.IsNotFound(err) {
		return []string{fmt.Sprintf("- %s is not found\n", obj)}, []string{}, nil
	}
	if err != nil {
		return []string{}, []string{}, err
	}

	curr := new(Object)
	err = unmarshallUnstructured(resp, curr)
	if err != nil {
		return []string{}, []string{}, err
	}
	diff := compare(obj, curr, opts...)
	if diff != "" {
		diffs = append(diffs, diff)
	}
	return []string{}, diffs, nil
}

func (d *Diff) diffList(resource schema.GroupVersionResource, obj *Object, opts ...cmp.Option) (presences, diffs []string, err error) {
	resp, err := d.client.
		Resource(resource).
		List(context.Background(), v1.ListOptions{})
	if err != nil {
		return []string{}, []string{}, err
	}

	m := make(map[string]*Object)
	checked := make(map[string]bool)
	for _, i := range resp.Items {
		curr := new(Object)
		err = unmarshallUnstructured(&i, curr)
		if err != nil {
			return []string{}, []string{}, err
		}
		m[curr.String()] = curr
		checked[curr.String()] = false
	}
	for _, i := range obj.Items {
		curr, ok := m[i.String()]
		if !ok {
			presences = append(presences, fmt.Sprintf("- %s is not found\n", i))
			continue
		}
		checked[curr.String()] = true
		diff := compare(i, curr, opts...)
		if diff == "" {
			continue
		}
		diffs = append(diffs, fmt.Sprintf("%s\n%s", i.String(), diff))
	}
	for k, v := range m {
		if !checked[k] {
			presences = append(presences, fmt.Sprintf("+ %s is found, but not in default\n", v))
		}
	}
	return presences, diffs, nil
}
