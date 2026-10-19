package cachesource

import (
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// recordedMembers is what a manager's status says about its members.
type recordedMembers struct {
	state ownership.MembershipState
	gitops.MemberList
}

func membersOf(gk resourceid.GroupKind, u *unstructured.Unstructured) recordedMembers {
	var list gitops.MemberList
	var ok bool
	switch gk {
	case ownership.ArgoApplication:
		list, ok = gitops.ArgoMembers(u)
	case ownership.FluxHelmRelease, ownership.FluxKustomization:
		list, ok = gitops.FluxMembers(u)
	}
	switch {
	case !ok:
		return recordedMembers{}
	case appliesHere(gk, u):
		return recordedMembers{state: ownership.MembersRecorded, MemberList: list}
	}
	return recordedMembers{state: ownership.MembersRemote, MemberList: list}
}

// appliesHere reports whether a manager's member list names objects of this
// cluster: an Argo CD list compared at the in-cluster destination, or a Flux
// object without spec.kubeConfig.
func appliesHere(gk resourceid.GroupKind, u *unstructured.Unstructured) bool {
	if gk == ownership.ArgoApplication {
		name, server, _ := gitops.ArgoComparedDestination(u)
		return gitops.IsInClusterArgoDestination(name, server)
	}
	return gitops.FluxTargetsLocalCluster(u)
}

func (s *session) Inventory(manager resourceid.Ref) ownership.InventoryRead {
	gk := manager.GroupKind()
	if !ownership.IsRecordingManager(gk) {
		return ownership.InventoryRead{Manager: unavailable(manager, readreason.ReadError)}
	}
	var members recordedMembers
	read := s.readObject(manager, func(u *unstructured.Unstructured) { members = membersOf(gk, u) })
	r := ownership.InventoryRead{Manager: read}
	if read.State != ownership.Present {
		return r
	}
	r.Membership = members.state
	if members.state == ownership.MembersUnreadable {
		return r
	}
	r.Members = make([]resourceid.Ref, 0, len(members.Entries))
	for _, e := range members.Entries {
		r.Members = append(r.Members, s.align(e, members.Destination))
	}
	slices.SortFunc(r.Members, compareRefs)
	r.Members = slices.Compact(r.Members)
	return r
}

// builtinKind returns the built-in kind group/kind names, if it is one.
func builtinKind(group, kind string) (resourceid.Builtin, bool) {
	b, ok := resourceid.BuiltinForKind(kind)
	return b, ok && b.Group == group
}

// scopeOf says whether gk is namespaced, and whether Radar can tell: a
// built-in kind's scope is its own unless discovery is ambiguous about the
// kind or serves it with the other scope; any other kind's is the catalog's.
func (s *session) scopeOf(gk resourceid.GroupKind) (namespaced, known bool) {
	if s.catalog.Ambiguous(gk) {
		return false, false
	}
	k, served := s.catalog.Kind(gk)
	if b, builtin := builtinKind(gk.Group, gk.Kind); builtin {
		return b.Namespaced, !served || k.Namespaced == b.Namespaced
	}
	return k.Namespaced, served
}

// align reads an entry as its kind's scope says, or as recorded when Radar
// can't tell the scope.
func (s *session) align(e gitops.Member, destination string) resourceid.Ref {
	ref := resourceid.Ref{Group: e.Group, Kind: e.Kind, Namespace: e.Namespace, Name: e.Name}
	if namespaced, known := s.scopeOf(ref.GroupKind()); known {
		ref.Namespace = gitops.MemberNamespace(namespaced, e.Namespace, destination)
	}
	return ref
}

// readings returns the GitOpsMembersIndex keys under which an entry naming ref
// is filed, as its kind's scope says, or why no entry can name ref.
func (s *session) readings(ref resourceid.Ref) ([]string, readreason.Code) {
	if ref.Kind == "" || ref.Name == "" {
		return nil, readreason.ReadError
	}
	gk := ref.GroupKind()
	namespaced, known := s.scopeOf(gk)
	_, builtin := builtinKind(ref.Group, ref.Kind)
	switch {
	case !known && (builtin || s.catalog.Ambiguous(gk) || !s.catalog.Complete()):
		// Radar can't tell the kind's scope.
		return nil, readreason.ReadError
	case !known:
		// No object of the kind can exist; entries name it as recorded.
		return []string{k8score.MemberKey(k8score.MemberAsRecorded, ref.Group, ref.Kind, ref.Namespace, ref.Name)}, ""
	case namespaced && ref.Namespace == "":
		return nil, readreason.NamespaceRequired
	case !namespaced && ref.Namespace != "":
		return nil, readreason.ReadError
	case builtin:
		return []string{k8score.MemberKey(k8score.MemberMeant, ref.Group, ref.Kind, ref.Namespace, ref.Name)}, ""
	case namespaced:
		return []string{k8score.MemberKey(k8score.MemberAsRecorded, ref.Group, ref.Kind, ref.Namespace, ref.Name), k8score.MemberKey(k8score.MemberInDestination, ref.Group, ref.Kind, ref.Namespace, ref.Name)}, ""
	}
	return []string{k8score.MemberKey(k8score.MemberAsRecorded, ref.Group, ref.Kind, "", ref.Name), k8score.MemberKey(k8score.MemberClusterScoped, ref.Group, ref.Kind, "", ref.Name)}, ""
}

func (s *session) Recorders(ref resourceid.Ref) ownership.RecorderRead {
	keys, reason := s.readings(ref)
	if reason != "" {
		return ownership.RecorderRead{Gaps: []ownership.Gap{{Kind: ref.GroupKind(), Namespace: ref.Namespace, Reason: reason}}}
	}
	q := &search{domain: ownership.Everywhere()}
	var recorders []candidate
	var elsewhere, unreadable []resourceid.Ref
	for _, gk := range ownership.RecordingManagers() {
		kind, served := s.catalog.Kind(gk)
		if !served {
			// Not installed, unless discovery missed it.
			if !s.catalog.Complete() || s.catalog.Ambiguous(gk) {
				q.gap(gk, "", readreason.ReadError)
			}
			continue
		}
		m := s.readMembers(kind, keys)
		recorders = append(recorders, m.recorders...)
		elsewhere = append(elsewhere, m.elsewhere...)
		unreadable = append(unreadable, m.unreadable...)
		q.gaps = append(q.gaps, m.gaps...)
	}
	var r ownership.RecorderRead
	slices.SortFunc(recorders, func(a, b candidate) int { return compareRefs(a.ref, b.ref) })
	recorders = slices.CompactFunc(recorders, func(a, b candidate) bool { return a.ref == b.ref })
	for _, c := range recorders {
		if read := observe(c.ref, c.apiVersion, c.obj); read.State == ownership.Present {
			r.Recorders = append(r.Recorders, read.Object)
		} else {
			q.gaps = append(q.gaps, read.Gap)
		}
	}
	slices.SortFunc(elsewhere, compareRefs)
	r.Elsewhere = slices.Compact(elsewhere)
	slices.SortFunc(unreadable, compareRefs)
	r.Unreadable = slices.Compact(unreadable)
	slices.SortFunc(q.gaps, compareGaps)
	r.Gaps = slices.Compact(q.gaps)
	return r
}

// membersRead is what one manager kind's index reads found.
type membersRead struct {
	recorders             []candidate
	elsewhere, unreadable []resourceid.Ref
	gaps                  []ownership.Gap
}

// readAttempts bounds how often readMembers reads a manager kind whose stores
// keep changing while it reads them.
const readAttempts = 3

// readMembers reads kind's unreadable list and the member keys from one state
// of its stores. A manager can change between two of these reads, from an
// unreadable list to one naming the object or from one reading of its entry to
// another, and a manager that changes twice can be in neither list at each
// read; so the reads count only if no store of kind changed while they ran.
// Without store versions to compare, it reads everything twice, in turn, which
// finds a manager that changes once mid-search.
func (s *session) readMembers(kind ownership.Kind, keys []string) membersRead {
	gr := schema.GroupResource{Group: kind.Group, Resource: kind.Resource}
	for range readAttempts {
		before, fenced := s.caches.Dynamic.StoreVersion(gr)
		var m membersRead
		s.readMembersOnce(&m, kind, keys)
		if !fenced {
			s.readMembersOnce(&m, kind, keys)
			return m
		}
		if after, _ := s.caches.Dynamic.StoreVersion(gr); after.Equal(before) {
			return m
		}
	}
	return membersRead{gaps: []ownership.Gap{{Kind: kind.GroupKind, Reason: readreason.KindSyncPending}}}
}

func (s *session) readMembersOnce(m *membersRead, kind ownership.Kind, keys []string) {
	gk := kind.GroupKind
	q := &search{domain: ownership.Everywhere()}
	s.dynamicIndexed(q, kind, k8score.GitOpsMembersIndex, k8score.UnreadableMembersKey, func(_ schema.GroupVersionResource, u *unstructured.Unstructured) {
		m.unreadable = append(m.unreadable, resourceid.Ref{Group: gk.Group, Kind: gk.Kind, Namespace: u.GetNamespace(), Name: u.GetName()})
	})
	s.afterRecordersRead(k8score.UnreadableMembersKey)
	for _, key := range keys {
		s.dynamicIndexed(q, kind, k8score.GitOpsMembersIndex, key, func(gvr schema.GroupVersionResource, u *unstructured.Unstructured) {
			manager := resourceid.Ref{Group: gk.Group, Kind: gk.Kind, Namespace: u.GetNamespace(), Name: u.GetName()}
			if !appliesHere(gk, u) {
				m.elsewhere = append(m.elsewhere, manager)
				return
			}
			m.recorders = append(m.recorders, candidate{ref: manager, apiVersion: gvr.GroupVersion().String(), obj: u})
		})
		s.afterRecordersRead(key)
	}
	m.gaps = append(m.gaps, q.gaps...)
}

// afterRecordersRead runs the session's recordersRead hook, if a test set one.
func (s *session) afterRecordersRead(key string) {
	if s.recordersRead != nil {
		s.recordersRead(key)
	}
}
