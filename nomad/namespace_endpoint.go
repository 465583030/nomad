// +build pro ent

package nomad

import (
	"fmt"
	"time"

	metrics "github.com/armon/go-metrics"
	memdb "github.com/hashicorp/go-memdb"
	"github.com/hashicorp/nomad/nomad/state"
	"github.com/hashicorp/nomad/nomad/structs"
)

// Namespace endpoint is used for manipulating namespaces
type Namespace struct {
	srv *Server
}

// UpsertNamespaces is used to upsert a set of namespaces
func (n *Namespace) UpsertNamespaces(args *structs.NamespaceUpsertRequest,
	reply *structs.GenericResponse) error {
	if done, err := n.srv.forward("Namespace.UpsertNamespaces", args, args, reply); done {
		return err
	}
	defer metrics.MeasureSince([]string{"nomad", "namespace", "upsert_namespaces"}, time.Now())

	// Check management permissions
	if aclObj, err := n.srv.resolveToken(args.SecretID); err != nil {
		return err
	} else if aclObj != nil && !aclObj.IsManagement() {
		return structs.ErrPermissionDenied
	}

	// Validate there is at least one namespace
	if len(args.Namespaces) == 0 {
		return fmt.Errorf("must specify at least one namespace")
	}

	// Validate the namespaces and set the hash
	for _, ns := range args.Namespaces {
		if err := ns.Validate(); err != nil {
			return fmt.Errorf("Invalid namespace %q: %v", ns.Name, err)
		}

		ns.SetHash()
	}

	// Update via Raft
	out, index, err := n.srv.raftApply(structs.NamespaceUpsertRequestType, args)
	if err != nil {
		return err
	}

	// Check if there was an error when applying.
	if err, ok := out.(error); ok && err != nil {
		return err
	}

	// Update the index
	reply.Index = index
	return nil
}

// DeleteNamespaces is used to delete a namespace
func (n *Namespace) DeleteNamespaces(args *structs.NamespaceDeleteRequest, reply *structs.GenericResponse) error {
	if done, err := n.srv.forward("Namespace.DeleteNamespaces", args, args, reply); done {
		return err
	}
	defer metrics.MeasureSince([]string{"nomad", "namespace", "delete_namespaces"}, time.Now())

	// Check management permissions
	if aclObj, err := n.srv.resolveToken(args.SecretID); err != nil {
		return err
	} else if aclObj != nil && !aclObj.IsManagement() {
		return structs.ErrPermissionDenied
	}

	// Validate at least one namespace
	if len(args.Namespaces) == 0 {
		return fmt.Errorf("must specify at least one namespace to delete")
	}

	for _, ns := range args.Namespaces {
		if ns == structs.DefaultNamespace {
			return fmt.Errorf("can not delete default namespace")
		}
	}

	// Update via Raft
	out, index, err := n.srv.raftApply(structs.NamespaceDeleteRequestType, args)
	if err != nil {
		return err
	}

	// Check if there was an error when applying.
	if err, ok := out.(error); ok && err != nil {
		return err
	}

	// Update the index
	reply.Index = index
	return nil
}

// ListNamespaces is used to list the namespaces
func (n *Namespace) ListNamespaces(args *structs.NamespaceListRequest, reply *structs.NamespaceListResponse) error {
	if done, err := n.srv.forward("Namespace.ListNamespaces", args, args, reply); done {
		return err
	}
	defer metrics.MeasureSince([]string{"nomad", "namespace", "list_namespace"}, time.Now())

	// Setup the blocking query
	opts := blockingOptions{
		queryOpts: &args.QueryOptions,
		queryMeta: &reply.QueryMeta,
		run: func(ws memdb.WatchSet, s *state.StateStore) error {
			// Iterate over all the namespaces
			var err error
			var iter memdb.ResultIterator
			if prefix := args.QueryOptions.Prefix; prefix != "" {
				iter, err = s.NamespacesByNamePrefix(ws, prefix)
			} else {
				iter, err = s.Namespaces(ws)
			}
			if err != nil {
				return err
			}

			reply.Namespaces = nil
			for {
				raw := iter.Next()
				if raw == nil {
					break
				}
				ns := raw.(*structs.Namespace)
				reply.Namespaces = append(reply.Namespaces, ns)
			}

			// Use the last index that affected the namespace table
			index, err := s.Index(state.TableNamespaces)
			if err != nil {
				return err
			}

			// Ensure we never set the index to zero, otherwise a blocking query cannot be used.
			// We floor the index at one, since realistically the first write must have a higher index.
			if index == 0 {
				index = 1
			}
			reply.Index = index
			return nil
		}}
	return n.srv.blockingRPC(&opts)
}

// GetNamespace is used to get a specific namespace
func (n *Namespace) GetNamespace(args *structs.NamespaceSpecificRequest, reply *structs.SingleNamespaceResponse) error {
	if done, err := n.srv.forward("Namespace.GetNamespace", args, args, reply); done {
		return err
	}
	defer metrics.MeasureSince([]string{"nomad", "namespace", "get_namespace"}, time.Now())

	// Check management permissions
	if aclObj, err := n.srv.resolveToken(args.SecretID); err != nil {
		return err
	} else if aclObj != nil && !aclObj.AllowNamespace(args.Name) {
		return structs.ErrPermissionDenied
	}

	// Setup the blocking query
	opts := blockingOptions{
		queryOpts: &args.QueryOptions,
		queryMeta: &reply.QueryMeta,
		run: func(ws memdb.WatchSet, s *state.StateStore) error {
			// Look for the namespace
			out, err := s.NamespaceByName(ws, args.Name)
			if err != nil {
				return err
			}

			// Setup the output
			reply.Namespace = out
			if out != nil {
				reply.Index = out.ModifyIndex
			} else {
				// Use the last index that affected the namespace table
				index, err := s.Index(state.TableNamespaces)
				if err != nil {
					return err
				}

				// Ensure we never set the index to zero, otherwise a blocking query cannot be used.
				// We floor the index at one, since realistically the first write must have a higher index.
				if index == 0 {
					index = 1
				}
				reply.Index = index
			}
			return nil
		}}
	return n.srv.blockingRPC(&opts)
}

// GetNamespaces is used to get a set of namespaces
func (n *Namespace) GetNamespaces(args *structs.NamespaceSetRequest, reply *structs.NamespaceSetResponse) error {
	if done, err := n.srv.forward("Namespace.GetNamespaces", args, args, reply); done {
		return err
	}
	defer metrics.MeasureSince([]string{"nomad", "namespace", "get_namespaces"}, time.Now())

	// Setup the blocking query
	opts := blockingOptions{
		queryOpts: &args.QueryOptions,
		queryMeta: &reply.QueryMeta,
		run: func(ws memdb.WatchSet, s *state.StateStore) error {
			// Setup the output
			reply.Namespaces = make(map[string]*structs.Namespace, len(args.Namespaces))

			// Look for the namespace
			for _, namespace := range args.Namespaces {
				out, err := s.NamespaceByName(ws, namespace)
				if err != nil {
					return err
				}
				if out != nil {
					reply.Namespaces[namespace] = out
				}
			}

			// Use the last index that affected the policy table
			index, err := s.Index(state.TableNamespaces)
			if err != nil {
				return err
			}

			// Ensure we never set the index to zero, otherwise a blocking query cannot be used.
			// We floor the index at one, since realistically the first write must have a higher index.
			if index == 0 {
				index = 1
			}
			reply.Index = index
			return nil
		}}
	return n.srv.blockingRPC(&opts)
}
