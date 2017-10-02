// +build ent

package nomad

import (
	"testing"
	"time"

	"github.com/hashicorp/nomad/nomad/mock"
	"github.com/hashicorp/nomad/nomad/state"
	"github.com/hashicorp/nomad/nomad/structs"
	"github.com/hashicorp/nomad/testutil"
	"github.com/stretchr/testify/assert"
)

func TestLeader_ReplicateSentinelPolicies(t *testing.T) {
	t.Parallel()
	s1, root := testACLServer(t, func(c *Config) {
		c.Region = "region1"
		c.AuthoritativeRegion = "region1"
		c.ACLEnabled = true
	})
	defer s1.Shutdown()
	s2, _ := testACLServer(t, func(c *Config) {
		c.Region = "region2"
		c.AuthoritativeRegion = "region1"
		c.ACLEnabled = true
		c.ReplicationBackoff = 20 * time.Millisecond
		c.ReplicationToken = root.SecretID
	})
	defer s2.Shutdown()
	testJoin(t, s1, s2)
	testutil.WaitForLeader(t, s1.RPC)
	testutil.WaitForLeader(t, s2.RPC)

	// Write a policy to the authoritative region
	p1 := mock.SentinelPolicy()
	if err := s1.State().UpsertSentinelPolicies(100, []*structs.SentinelPolicy{p1}); err != nil {
		t.Fatalf("bad: %v", err)
	}

	// Wait for the policy to replicate
	testutil.WaitForResult(func() (bool, error) {
		state := s2.State()
		out, err := state.SentinelPolicyByName(nil, p1.Name)
		return out != nil, err
	}, func(err error) {
		t.Fatalf("should replicate policy")
	})

	// Delete the namespace at the authoritative region
	assert.Nil(t, s1.State().DeleteSentinelPolicies(200, []string{p1.Name}))

	// Wait for the deletion to replicate
	testutil.WaitForResult(func() (bool, error) {
		state := s2.State()
		out, err := state.SentinelPolicyByName(nil, p1.Name)
		return out == nil, err
	}, func(err error) {
		t.Fatalf("should replicate policy deletion")
	})
}

func TestLeader_DiffSentinelPolicies(t *testing.T) {
	t.Parallel()

	state := state.TestStateStore(t)

	// Populate the local state
	p1 := mock.SentinelPolicy()
	p2 := mock.SentinelPolicy()
	p3 := mock.SentinelPolicy()
	assert.Nil(t, state.UpsertSentinelPolicies(100, []*structs.SentinelPolicy{p1, p2, p3}))

	// Simulate a remote list
	p2Stub := p2.Stub()
	p2Stub.ModifyIndex = 50 // Ignored, same index
	p3Stub := p3.Stub()
	p3Stub.ModifyIndex = 100 // Updated, higher index
	p3Stub.Hash = []byte{0, 1, 2, 3}
	p4 := mock.SentinelPolicy()
	remoteList := []*structs.SentinelPolicyListStub{
		p2Stub,
		p3Stub,
		p4.Stub(),
	}
	delete, update := diffSentinelPolicies(state, 50, remoteList)

	// P1 does not exist on the remote side, should delete
	assert.Equal(t, []string{p1.Name}, delete)

	// P2 is un-modified - ignore. P3 modified, P4 new.
	assert.Equal(t, []string{p3.Name, p4.Name}, update)
}
