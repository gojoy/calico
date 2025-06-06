// Copyright (c) 2016-2024 Tigera, Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package model

import (
	"bytes"
	"fmt"
	net2 "net"
	"reflect"
	"strings"
	"time"

	apiv3 "github.com/projectcalico/api/pkg/apis/projectcalico/v3"
	log "github.com/sirupsen/logrus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/projectcalico/calico/libcalico-go/lib/json"
	"github.com/projectcalico/calico/libcalico-go/lib/namespace"
	"github.com/projectcalico/calico/libcalico-go/lib/net"
)

const (
	metadataAnnotation = "projectcalico.org/metadata"
)

// RawString is used a value type to indicate that the value is a bare non-JSON string
type rawString string
type rawBool bool
type rawIP net.IP

var rawStringType = reflect.TypeOf(rawString(""))
var rawBoolType = reflect.TypeOf(rawBool(true))
var rawIPType = reflect.TypeOf(rawIP{})

// Key represents a parsed datastore key.
type Key interface {
	// defaultPath() returns a common path representation of the object used by
	// etcdv3 and other datastores.
	defaultPath() (string, error)

	// defaultDeletePath() returns a common path representation used by etcdv3
	// and other datastores to delete the object.
	defaultDeletePath() (string, error)

	// defaultDeleteParentPaths() returns an ordered slice of paths that should
	// be removed after deleting the primary path (given by defaultDeletePath),
	// provided there are no child entries associated with those paths.  This is
	// only used by directory based KV stores (such as etcdv3).  With a directory
	// based KV store, creation of a resource may also create parent directory entries
	// that could be shared by multiple resources, and therefore the parent directories
	// can only be removed when there are no more resources under them.  The list of
	// parent paths is ordered, and directories should be removed in the order supplied
	// in the slice and only if the directory is empty.
	defaultDeleteParentPaths() ([]string, error)

	// valueType returns the object type associated with this key.
	valueType() (reflect.Type, error)

	// String returns a unique string representation of this key.  The string
	// returned by this method must uniquely identify this Key.
	String() string
}

// Interface used to perform datastore lookups.
type ListInterface interface {
	// defaultPathRoot() returns a default stringified root path, i.e. path
	// to the directory containing all the keys to be listed.
	defaultPathRoot() string

	// BUG(smc) I think we should remove this and use the package KeyFromDefaultPath function.
	// KeyFromDefaultPath parses the default path representation of the
	// Key type for this list.  It returns nil if passed a different kind
	// of path.
	KeyFromDefaultPath(key string) Key
}

// KVPair holds a typed key and value object as well as datastore specific
// revision information.
//
// The Value is dependent on the Key, but in general will be one of the following
// types:
//   - A pointer to a struct
//   - A slice or map
//   - A bare string, boolean value or IP address (i.e. without quotes, so not
//     JSON format).
type KVPair struct {
	Key      Key
	Value    interface{}
	Revision string
	UID      *types.UID
	TTL      time.Duration // For writes, if non-zero, key has a TTL.
}

// KVPairList hosts a slice of KVPair structs and a Revision, returned from a Ls
type KVPairList struct {
	KVPairs  []*KVPair
	Revision string
}

// KeyToDefaultPath converts one of the Keys from this package into a unique
// '/'-delimited path, which is suitable for use as the key when storing the
// value in a hierarchical (i.e. one with directories and leaves) key/value
// datastore such as etcd v3.
//
// Each unique key returns a unique path.
//
// Keys with a hierarchical relationship share a common prefix.  However, in
// order to support datastores that do not support storing data at non-leaf
// nodes in the hierarchy (such as etcd v3), the path returned for a "parent"
// key, is not a direct ancestor of its children.
func KeyToDefaultPath(key Key) (string, error) {
	return key.defaultPath()
}

// KeyToDefaultDeletePath converts one of the Keys from this package into a
// unique '/'-delimited path, which is suitable for use as the key when
// (recursively) deleting the value from a hierarchical (i.e. one with
// directories and leaves) key/value datastore such as etcd v3.
//
// KeyToDefaultDeletePath returns a different path to KeyToDefaultPath when
// it is a passed a Key that represents a non-leaf, such as a TierKey.  (A
// tier has its own metadata but it also contains policies as children.)
//
// KeyToDefaultDeletePath returns the common prefix of the non-leaf key and
// its children so that a recursive delete of that key would delete the
// object itself and any children it has.
//
// For example, KeyToDefaultDeletePath(TierKey{Tier: "a"}) returns
//
//	"/calico/v1/policy/tier/a"
//
// which is a prefix of both KeyToDefaultPath(TierKey{Tier: "a"}):
//
//	"/calico/v1/policy/tier/a/metadata"
//
// and KeyToDefaultPath(PolicyKey{Tier: "a", Name: "b"}):
//
//	"/calico/v1/policy/tier/a/policy/b"
func KeyToDefaultDeletePath(key Key) (string, error) {
	return key.defaultDeletePath()
}

// KeyToDefaultDeleteParentPaths returns a slice of '/'-delimited
// paths which are used to delete parent entries that may be auto-created
// by directory-based KV stores (e.g. etcd v3).  These paths should also be
// removed provided they have no more child entries.
//
// The list of parent paths is ordered, and directories should be removed
// in the order supplied in the slice and only if the directory is empty.
//
// For example,
//
//	KeyToDefaultDeletePaths(WorkloadEndpointKey{
//		Nodename: "h",
//		OrchestratorID: "o",
//		WorkloadID: "w",
//		EndpointID: "e",
//	})
//
// returns
//
// ["/calico/v1/host/h/workload/o/w/endpoint",
//
//	"/calico/v1/host/h/workload/o/w"]
//
// indicating that these paths should also be deleted when they are empty.
// In this example it is equivalent to deleting the workload when there are
// no more endpoints in the workload.
func KeyToDefaultDeleteParentPaths(key Key) ([]string, error) {
	return key.defaultDeleteParentPaths()
}

// ListOptionsToDefaultPathRoot converts list options struct into a
// common-prefix path suitable for querying a datastore that uses the paths
// returned by KeyToDefaultPath.  For example,
//
//	ListOptionsToDefaultPathRoot(TierListOptions{})
//
// doesn't specify any particular tier so it returns
// "/calico/v1/policy/tier" which is a prefix for all tiers.  The datastore
// must then do a recursive query to find all children of that path.
// However,
//
//	ListOptionsToDefaultPathRoot(TierListOptions{Tier:"a"})
//
// returns a more-specific path, which filters down to the specific tier of
// interest: "/calico/v1/policy/tier/a"
func ListOptionsToDefaultPathRoot(listOptions ListInterface) string {
	return listOptions.defaultPathRoot()
}

// ListOptionsIsFullyQualified returns true if the options actually specify a fully
// qualified resource rather than a partial match.
func ListOptionsIsFullyQualified(listOptions ListInterface) bool {
	// Construct the path prefix and then check to see if that actually corresponds to
	// the path of a resource instance.
	return listOptions.KeyFromDefaultPath(listOptions.defaultPathRoot()) != nil
}

// IsListOptionsLastSegmentPrefix returns true if the final segment of the default path
// root is a name prefix rather than the full name.
func IsListOptionsLastSegmentPrefix(listOptions ListInterface) bool {
	// Only supported for ResourceListOptions.
	rl, ok := listOptions.(ResourceListOptions)
	return ok && rl.IsLastSegmentIsPrefix()
}

// KeyFromDefaultPath parses the default path representation of a key into one
// of our <Type>Key structs.  Returns nil if the string doesn't match one of
// our key types.
func KeyFromDefaultPath(path string) Key {
	// "v3" resource keys strictly require a leading slash but older "v1" keys were permissive.
	// For ease of parsing, strip the slash off now but pass it down to keyFromDefaultPathInner so
	// it can check for it later.
	normalizedPath := path
	if strings.HasPrefix(normalizedPath, "/") {
		normalizedPath = normalizedPath[1:]
	}

	parts := strings.Split(normalizedPath, "/")
	if len(parts) < 3 {
		// After removing the optional `/` prefix, should have at least 3 segments.
		return nil
	}

	return keyFromDefaultPathInner(path, parts)
}

func keyFromDefaultPathInner(path string, parts []string) Key {
	if parts[0] != "calico" {
		return nil
	}

	switch parts[1] {
	case "v1":
		switch parts[2] {
		case "ipam":
			return IPPoolListOptions{}.KeyFromDefaultPath(path)
		case "config":
			return GlobalConfigKey{Name: strings.Join(parts[3:], "/")}
		case "host":
			if len(parts) < 5 {
				return nil
			}
			hostname := parts[3]
			switch parts[4] {
			case "workload":
				if len(parts) != 9 || parts[7] != "endpoint" {
					return nil
				}
				return WorkloadEndpointKey{
					Hostname:       unescapeName(hostname),
					OrchestratorID: unescapeName(parts[5]),
					WorkloadID:     unescapeName(parts[6]),
					EndpointID:     unescapeName(parts[8]),
				}
			case "endpoint":
				if len(parts) != 6 {
					return nil
				}
				return HostEndpointKey{
					Hostname:   unescapeName(hostname),
					EndpointID: unescapeName(parts[5]),
				}
			case "config":
				return HostConfigKey{
					Hostname: unescapeName(hostname),
					Name:     strings.Join(parts[5:], "/"),
				}
			case "metadata":
				if len(parts) != 5 {
					return nil
				}
				return HostMetadataKey{
					Hostname: hostname,
				}
			case "bird_ip":
				if len(parts) != 5 {
					return nil
				}
				return HostIPKey{
					Hostname: unescapeName(hostname),
				}
			case "wireguard":
				if len(parts) != 5 {
					return nil
				}
				return WireguardKey{
					NodeName: unescapeName(hostname),
				}
			}
		case "netset":
			if len(parts) != 4 {
				return nil
			}
			return NetworkSetKey{
				Name: unescapeName(parts[3]),
			}
		case "Ready":
			if len(parts) > 3 || path[0] != '/' {
				return nil
			}
			return ReadyFlagKey{}
		case "policy":
			if len(parts) < 6 {
				return nil
			}
			switch parts[3] {
			case "tier":
				if len(parts) < 6 {
					return nil
				}
				switch parts[5] {
				case "metadata":
					if len(parts) != 6 {
						return nil
					}
					return TierKey{
						Name: unescapeName(parts[4]),
					}
				case "policy":
					if len(parts) != 7 {
						return nil
					}
					return PolicyKey{
						Tier: unescapeName(parts[4]),
						Name: unescapeName(parts[6]),
					}
				}
			case "profile":
				pk := unescapeName(parts[4])
				switch parts[5] {
				case "rules":
					return ProfileRulesKey{ProfileKey: ProfileKey{pk}}
				case "labels":
					return ProfileLabelsKey{ProfileKey: ProfileKey{pk}}
				}
			}
		}
	case "bgp":
		switch parts[2] {
		case "v1":
			if len(parts) < 5 {
				return nil
			}
			switch parts[3] {
			case "global":
				return GlobalBGPConfigListOptions{}.KeyFromDefaultPath(path)
			case "host":
				if len(parts) < 6 {
					return nil
				}
				return NodeBGPConfigListOptions{}.KeyFromDefaultPath(path)
			}
		}
	case "ipam":
		if len(parts) < 5 {
			return nil
		}
		switch parts[2] {
		case "v2":
			switch parts[3] {
			case "assignment":
				return BlockListOptions{}.KeyFromDefaultPath(path)
			case "handle":
				if len(parts) > 5 {
					return nil
				}
				return IPAMHandleKey{
					HandleID: parts[4],
				}
			case "host":
				return BlockAffinityListOptions{}.KeyFromDefaultPath(path)
			}
		}
	case "resources":
		switch parts[2] {
		case "v3":
			// v3 resource keys strictly require the leading slash.
			if len(parts) < 6 || parts[3] != "projectcalico.org" || path[0] != '/' {
				return nil
			}
			switch len(parts) {
			case 6:
				ri, ok := resourceInfoByPlural[unescapeName(parts[4])]
				if !ok {
					log.Warnf("(BUG) unknown resource type: %v", path)
					return nil
				}
				if namespace.IsNamespaced(ri.kind) {
					log.Warnf("(BUG) Path is a global resource, but resource is namespaced: %v", path)
					return nil
				}
				log.Debugf("Path is a global resource: %v", path)
				return ResourceKey{
					Kind: ri.kind,
					Name: unescapeName(parts[5]),
				}
			case 7:
				ri, ok := resourceInfoByPlural[unescapeName(parts[4])]
				if !ok {
					log.Warnf("(BUG) unknown resource type: %v", path)
					return nil
				}
				if !namespace.IsNamespaced(ri.kind) {
					log.Warnf("(BUG) Path is a namespaced resource, but resource is global: %v", path)
					return nil
				}
				log.Debugf("Path is a namespaced resource: %v", path)
				return ResourceKey{
					Kind:      ri.kind,
					Namespace: unescapeName(parts[5]),
					Name:      unescapeName(parts[6]),
				}
			}
		}
	case "felix":
		if len(parts) < 4 {
			return nil
		}
		switch parts[2] {
		case "v1":
			switch parts[3] {
			case "host":
				if len(parts) != 7 || parts[5] != "endpoint" {
					return nil
				}
				return HostEndpointStatusKey{
					Hostname:   parts[4],
					EndpointID: unescapeName(parts[6]),
				}
			}
		case "v2":
			if len(parts) < 7 {
				return nil
			}
			if parts[4] != "host" {
				return nil
			}
			switch parts[6] {
			case "status":
				return ActiveStatusReportListOptions{}.KeyFromDefaultPath(path)
			case "last_reported_status":
				return LastStatusReportListOptions{}.KeyFromDefaultPath(path)
			case "workload":
				return WorkloadEndpointStatusListOptions{}.KeyFromDefaultPath(path)
			}
		}
	}
	log.Debugf("Path is unknown: %v", path)
	return nil
}

// OldKeyFromDefaultPath is the old, (slower) implementation of KeyFromDefaultPath.  It is kept to allow
// fuzzing the new version against it.  Parses the default path representation of a key into one
// of our <Type>Key structs.  Returns nil if the string doesn't match one of
// our key types.
func OldKeyFromDefaultPath(path string) Key {
	if m := matchWorkloadEndpoint.FindStringSubmatch(path); m != nil {
		log.Debugf("Path is a workload endpoint: %v", path)
		return WorkloadEndpointKey{
			Hostname:       unescapeName(m[1]),
			OrchestratorID: unescapeName(m[2]),
			WorkloadID:     unescapeName(m[3]),
			EndpointID:     unescapeName(m[4]),
		}
	} else if m := matchHostEndpoint.FindStringSubmatch(path); m != nil {
		log.Debugf("Path is a host endpoint: %v", path)
		return HostEndpointKey{
			Hostname:   unescapeName(m[1]),
			EndpointID: unescapeName(m[2]),
		}
	} else if m := matchNetworkSet.FindStringSubmatch(path); m != nil {
		log.Debugf("Path is a network set: %v", path)
		return NetworkSetKey{
			Name: unescapeName(m[1]),
		}
	} else if m := matchGlobalResource.FindStringSubmatch(path); m != nil {
		ri, ok := resourceInfoByPlural[unescapeName(m[1])]
		if !ok {
			log.Warnf("(BUG) unknown resource type: %v", path)
			return nil
		}
		if namespace.IsNamespaced(ri.kind) {
			log.Warnf("(BUG) Path is a global resource, but resource is namespaced: %v", path)
			return nil
		}
		log.Debugf("Path is a global resource: %v", path)
		return ResourceKey{
			Kind: ri.kind,
			Name: unescapeName(m[2]),
		}
	} else if m := matchNamespacedResource.FindStringSubmatch(path); m != nil {
		ri, ok := resourceInfoByPlural[unescapeName(m[1])]
		if !ok {
			log.Warnf("(BUG) unknown resource type: %v", path)
			return nil
		}
		if !namespace.IsNamespaced(ri.kind) {
			log.Warnf("(BUG) Path is a namespaced resource, but resource is global: %v", path)
			return nil
		}
		log.Debugf("Path is a namespaced resource: %v", path)
		return ResourceKey{
			Kind:      resourceInfoByPlural[unescapeName(m[1])].kind,
			Namespace: unescapeName(m[2]),
			Name:      unescapeName(m[3]),
		}
	} else if m := matchPolicy.FindStringSubmatch(path); m != nil {
		log.Debugf("Path is a policy: %v", path)
		return PolicyKey{
			Tier: unescapeName(m[1]),
			Name: unescapeName(m[2]),
		}
	} else if m := matchProfile.FindStringSubmatch(path); m != nil {
		log.Debugf("Path is a profile: %v (%v)", path, m[2])
		pk := ProfileKey{unescapeName(m[1])}
		switch m[2] {
		case "rules":
			log.Debugf("Profile rules")
			return ProfileRulesKey{ProfileKey: pk}
		case "labels":
			log.Debugf("Profile labels")
			return ProfileLabelsKey{ProfileKey: pk}
		}
		return nil
	} else if m := matchTier.FindStringSubmatch(path); m != nil {
		log.Debugf("Path is a policy tier: %v", path)
		return TierKey{Name: m[1]}
	} else if m := matchHostIp.FindStringSubmatch(path); m != nil {
		log.Debugf("Path is a host ID: %v", path)
		return HostIPKey{Hostname: unescapeName(m[1])}
	} else if m := matchWireguard.FindStringSubmatch(path); m != nil {
		log.Debugf("Path is a node name: %v", path)
		return WireguardKey{NodeName: unescapeName(m[1])}
	} else if m := matchIPPool.FindStringSubmatch(path); m != nil {
		log.Debugf("Path is a pool: %v", path)
		mungedCIDR := m[1]
		cidr := strings.Replace(mungedCIDR, "-", "/", 1)
		_, c, err := net.ParseCIDR(cidr)
		if err != nil {
			log.WithError(err).Warningf("Failed to parse CIDR %s", cidr)
		} else {
			return IPPoolKey{CIDR: *c}
		}
	} else if m := matchGlobalConfig.FindStringSubmatch(path); m != nil {
		log.Debugf("Path is a global felix config: %v", path)
		return GlobalConfigKey{Name: m[1]}
	} else if m := matchHostConfig.FindStringSubmatch(path); m != nil {
		log.Debugf("Path is a host config: %v", path)
		return HostConfigKey{Hostname: unescapeName(m[1]), Name: m[2]}
	} else if matchReadyFlag.MatchString(path) {
		log.Debugf("Path is a ready flag: %v", path)
		return ReadyFlagKey{}
	} else if k := (NodeBGPConfigListOptions{}).KeyFromDefaultPath(path); k != nil {
		return k
	} else if k := (GlobalBGPConfigListOptions{}).KeyFromDefaultPath(path); k != nil {
		return k
	} else if k := (BlockAffinityListOptions{}).KeyFromDefaultPath(path); k != nil {
		return k
	} else if k := (BlockListOptions{}).KeyFromDefaultPath(path); k != nil {
		return k
	} else if k := (HostEndpointStatusListOptions{}).KeyFromDefaultPath(path); k != nil {
		return k
	} else if k := (WorkloadEndpointStatusListOptions{}).KeyFromDefaultPath(path); k != nil {
		return k
	} else if k := (ActiveStatusReportListOptions{}).KeyFromDefaultPath(path); k != nil {
		return k
	} else if k := (LastStatusReportListOptions{}).KeyFromDefaultPath(path); k != nil {
		return k
	} else {
		log.Debugf("Path is unknown: %v", path)
	}
	// Not a key we know about.
	return nil
}

// ParseValue parses the default JSON representation of our data into one of
// our value structs, according to the type of key.  I.e. if passed a
// PolicyKey as the first parameter, it will try to parse rawData into a
// Policy struct.
func ParseValue(key Key, rawData []byte) (interface{}, error) {
	valueType, err := key.valueType()
	if err != nil {
		return nil, err
	}
	if valueType == rawStringType {
		return string(rawData), nil
	}
	if valueType == rawBoolType {
		return string(rawData) == "true", nil
	}
	if valueType == rawIPType {
		ip := net2.ParseIP(string(rawData))
		if ip == nil {
			return nil, nil
		}
		return &net.IP{IP: ip}, nil
	}
	value := reflect.New(valueType)
	elem := value.Elem()
	if elem.Kind() == reflect.Struct && elem.NumField() > 0 {
		if elem.Field(0).Type() == reflect.ValueOf(key).Type() {
			elem.Field(0).Set(reflect.ValueOf(key))
		}
	}
	iface := value.Interface()
	err = json.Unmarshal(rawData, iface)
	if err != nil {
		// This is a special case to address backwards compatibility from the time when we had no state information as block affinity value.
		// example:
		// Key: "/calico/ipam/v2/host/myhost.io/ipv4/block/172.29.82.0-26"
		// Value: ""
		// In 3.0.7 we added block affinity state as the value, so old "" value is no longer a valid JSON, so for that
		// particular case we replace the "" with a "{}" so it can be parsed and we don't leak blocks after upgrade to Calico 3.0.7
		// See: https://github.com/projectcalico/calico/issues/1956
		if bytes.Equal(rawData, []byte(``)) && valueType == typeBlockAff {
			rawData = []byte(`{}`)
			if err = json.Unmarshal(rawData, iface); err != nil {
				return nil, err
			}
		} else {
			log.Warningf("Failed to unmarshal %#v into value %#v",
				string(rawData), value)
			return nil, err
		}
	}

	if elem.Kind() != reflect.Struct {
		// Pointer to a map or slice, unwrap.
		iface = elem.Interface()
	}

	if valueType == reflect.TypeOf(apiv3.NetworkPolicy{}) {
		policy := iface.(*apiv3.NetworkPolicy)
		policy.Name, policy.Annotations, err = determinePolicyName(policy.Name, policy.Spec.Tier, policy.Annotations)
		if err != nil {
			return nil, err
		}
	}

	if valueType == reflect.TypeOf(apiv3.GlobalNetworkPolicy{}) {
		policy := iface.(*apiv3.GlobalNetworkPolicy)
		policy.Name, policy.Annotations, err = determinePolicyName(policy.Name, policy.Spec.Tier, policy.Annotations)
		if err != nil {
			return nil, err
		}
	}

	if valueType == reflect.TypeOf(apiv3.StagedNetworkPolicy{}) {
		policy := iface.(*apiv3.StagedNetworkPolicy)
		policy.Name, policy.Annotations, err = determinePolicyName(policy.Name, policy.Spec.Tier, policy.Annotations)
		if err != nil {
			return nil, err
		}
	}

	if valueType == reflect.TypeOf(apiv3.StagedGlobalNetworkPolicy{}) {
		policy := iface.(*apiv3.StagedGlobalNetworkPolicy)
		policy.Name, policy.Annotations, err = determinePolicyName(policy.Name, policy.Spec.Tier, policy.Annotations)
		if err != nil {
			return nil, err
		}
	}

	return iface, nil
}

// SerializeValue serializes a value in the model to a []byte to be stored in the datastore.  This
// performs the opposite processing to ParseValue()
func SerializeValue(d *KVPair) ([]byte, error) {
	valueType, err := d.Key.valueType()
	if err != nil {
		return nil, err
	}
	if d.Value == nil {
		return json.Marshal(nil)
	}
	if valueType == rawStringType {
		return []byte(d.Value.(string)), nil
	}
	if valueType == rawBoolType {
		return []byte(fmt.Sprint(d.Value)), nil
	}
	if valueType == rawIPType {
		return []byte(fmt.Sprint(d.Value)), nil
	}
	return json.Marshal(d.Value)
}

// determinePolicyName updates Policy name based on either the projectcalico.org/metadata annotation that was added in 3.30,
// or defaults the name to be returned without the default prefix if no annotation was found. This was the default behaviour in =<3.28
func determinePolicyName(name, tier string, annotations map[string]string) (string, map[string]string, error) {
	if annotations != nil && annotations[metadataAnnotation] != "" {
		meta := &metav1.ObjectMeta{}
		err := json.Unmarshal([]byte(annotations[metadataAnnotation]), meta)
		if err != nil {
			return "", nil, err
		}
		delete(annotations, metadataAnnotation)
		return meta.Name, annotations, nil
	}

	if tier == "default" || tier == "" {
		// It's possible the policy does not contain tier, that means it's in the default Tier it's added later by the API server
		return strings.TrimPrefix(name, "default."), annotations, nil
	}

	return name, annotations, nil
}
