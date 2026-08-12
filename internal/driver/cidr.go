package driver

// cidr.go is the cidr driver: a subnet sliced from a pool by slot
// (03-drivers.md §4.3). The derivation is pure arithmetic — slot 1 takes
// the first block, each block is 2^(32-size) addresses — and lives in
// spec.Resolve, which this driver agrees with by construction, so the
// downstream allocator is size-agnostic.
//
// The rails of §4.3 bind the rest: the probe asks whether any existing
// network on the reachable daemon overlaps the derived slice (unavailable
// without a socket, which does not block allocation); teardown is none —
// the network belongs to the namespace driver's project; and exhaustion
// falls back to the shared pool loudly — a warning naming the remedy, or
// fails outright per on_exhaustion — never silently, because a silent
// fallback reads as success (D7, plan.md §3).
//
// Nothing reallocates a fallen-back cidr when a slot later frees: the
// fallback value is recorded in the registry at allocation and never
// re-derived, so the warning this driver emits persists for the life of
// the worktree (03-drivers.md §8, open question 5).

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// CIDR is the cidr driver.
type CIDR struct{}

// Type reports the resource type.
func (*CIDR) Type() string { return "cidr" }

// HasApply reports that the application routes its own traffic; the tool
// never creates a network (03-drivers.md §2.1's no-apply column).
func (*CIDR) HasApply() bool { return false }

// HasTeardown reports none: the network belongs to the namespace driver's
// project and leaves with it (03-drivers.md §4.3).
func (*CIDR) HasTeardown() bool { return false }

// GatesAllocation reports that a Held probe skips the candidate slot: a
// derived slice that overlaps an existing network is not usable, and the
// allocator says which slot it skipped (B1.6's rule applied to subnets).
func (*CIDR) GatesAllocation() bool { return true }

// Derive returns the resolved slice, exactly as spec.Resolve computes it —
// including the shared-pool fallback value when the pool is exhausted and
// on_exhaustion allows it.
func (*CIDR) Derive(r *spec.Resource, s *spec.Spec, ctx spec.Context) (any, error) {
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		return nil, err
	}
	v, ok := table[r.Name]
	if !ok {
		return nil, fmt.Errorf("cidr driver: no resolved value for resource %q", r.Name)
	}
	return v.Value, nil
}

// Probe reports whether any existing network on the reachable daemon
// overlaps the derived slice. Unavailable without a socket, and an
// unavailable probe does not block allocation (plan.md §3).
//
// A fallen-back value — the shared pool itself — always probes free: the
// worktree shares the pool deliberately, overlap is the point rather than
// a collision, and holding the slot would make the loud fallback of
// §4.3 unreachable (every slot past the pool would probe held and the
// allocator would report plain exhaustion instead).
func (*CIDR) Probe(r *spec.Resource, value any, env Env) ProbeResult {
	cidr, ok := value.(string)
	if !ok || cidr == "" {
		return ProbeUnavailable
	}
	if cidrFellBack(r, cidr, env.Slot) {
		return ProbeFree
	}
	if env.Docker == nil {
		return ProbeUnavailable
	}
	if err := env.Docker.Version(); err != nil {
		return ProbeUnavailable
	}
	names, err := env.Docker.ListNetworksAll()
	if err != nil {
		return ProbeUnavailable
	}
	for _, name := range names {
		subnet, serr := env.Docker.NetworkSubnet(name)
		if serr != nil {
			continue // one unreadable network does not hold the slot
		}
		if cidrsOverlap(cidr, subnet) {
			return ProbeHeld
		}
	}
	return ProbeFree
}

// Apply is the optional half's absent side: the application routes its own
// traffic, and the tool never creates a network.
func (*CIDR) Apply(*spec.Resource, any, Env) (ApplyResult, error) { return ApplyResult{}, nil }

// Teardown is the optional half's absent side: the network belongs to the
// namespace driver's project (03-drivers.md §4.3).
func (*CIDR) Teardown(*spec.Resource, any, Env) error { return nil }

// Verify reports the one drift a cidr can carry: the value fell back to
// the shared pool, and nothing will reallocate it, so the warning persists
// for the life of the worktree. Verify changes nothing.
func (*CIDR) Verify(r *spec.Resource, value any, env Env) ([]Finding, error) {
	cidr, ok := value.(string)
	if !ok || cidr == "" {
		return nil, nil
	}
	if note := FallbackNote(r, cidr, env.Slot); note != "" {
		return []Finding{{Resource: r.Name, Level: LevelWarning, Message: note}}, nil
	}
	return nil, nil
}

// FallbackNote returns the loud warning for a cidr whose recorded value is
// the shared-pool fallback, or "" when the value is a real slice of the
// pool. The remedy is named: free a slot, or widen the pool in the spec.
// A fallback that is silent is the failure this rule exists to prevent
// (D7), so the allocation path and doctor both emit this note — the
// warning persists for the life of the worktree because nothing
// reallocates a fallen-back cidr when a slot later frees (03-drivers.md
// §4.3, §8).
func FallbackNote(r *spec.Resource, value string, slot int) string {
	if !cidrFellBack(r, value, slot) {
		return ""
	}
	_, poolNet, err := net.ParseCIDR(*r.Pool)
	if err != nil {
		return ""
	}
	block, poolSize := cidrBlockCounts(*r.Pool, *r.Size)
	return fmt.Sprintf(
		"the pool %s is exhausted: slot %d is past its last block (the pool holds %d block(s) of /%d), so this worktree falls back to the shared pool %s — every fallen-back worktree shares it, and nothing reallocates it when a slot later frees; free a slot ('wt rm --slug <slug>' or 'wt cleanup'), or widen the pool in wt.yaml, to give this worktree its own subnet",
		poolNet.String(), slot, poolSize/block, *r.Size, value)
}

// cidrFellBack reports whether the recorded value is the shared-pool
// fallback for this resource and slot: the pool network with the pool's
// mask, while the slot's own block lies outside the pool. The slot check
// is what keeps the value unambiguous — a spec with size equal to the
// pool's mask derives the pool network itself for slot 1, and only the
// exhaustion arithmetic tells the two apart.
func cidrFellBack(r *spec.Resource, value string, slot int) bool {
	if r == nil || r.Pool == nil || r.Size == nil {
		return false
	}
	_, poolNet, err := net.ParseCIDR(*r.Pool)
	if err != nil {
		return false
	}
	poolBits, _ := poolNet.Mask.Size()
	want := poolNet.IP.String() + "/" + strconv.Itoa(poolBits)
	if value != want {
		return false
	}
	block, poolSize := cidrBlockCounts(*r.Pool, *r.Size)
	return uint64(slot-1)*block+block > poolSize
}

// cidrBlockCounts returns the block size and pool size in addresses for a
// pool and per-slot mask. The caller has already validated both, so the
// parse cannot fail; a pathological spec returns zeroes.
func cidrBlockCounts(pool string, size int) (block, poolSize uint64) {
	_, poolNet, err := net.ParseCIDR(pool)
	if err != nil {
		return 0, 0
	}
	poolBits, _ := poolNet.Mask.Size()
	if size < poolBits || size > 32 {
		return 0, 0
	}
	return uint64(1) << (32 - size), uint64(1) << (32 - poolBits)
}

// cidrsOverlap reports whether two IPv4 CIDR ranges share any address. A
// network with an unparseable or non-IPv4 subnet never overlaps.
func cidrsOverlap(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	_, an, err := net.ParseCIDR(a)
	if err != nil {
		return false
	}
	_, bn, err := net.ParseCIDR(b)
	if err != nil {
		return false
	}
	aStart := ip4ToUint32(an.IP)
	aEnd := aStart + ip4Range(an)
	bStart := ip4ToUint32(bn.IP)
	bEnd := bStart + ip4Range(bn)
	return aStart < bEnd && bStart < aEnd
}

// ip4Range returns the number of addresses a network covers, exclusive of
// its network address (the count of usable + unusable addresses is
// 2^(32-bits), so two ranges overlap when their half-open intervals do).
func ip4Range(n *net.IPNet) uint64 {
	bits, _ := n.Mask.Size()
	return uint64(1) << (32 - bits)
}

// ip4ToUint32 turns an IPv4 address into the uint32 the range arithmetic
// uses. The driver's own copy of the spec package's helper, because the
// overlap check is driver-side arithmetic over parsed CIDRs.
func ip4ToUint32(ip net.IP) uint64 {
	ip = ip.To4()
	return uint64(ip[0])<<24 | uint64(ip[1])<<16 | uint64(ip[2])<<8 | uint64(ip[3])
}

// BlastRadius is the shared-block prose for a cidr resource. A cidr cannot
// declare default: shared (the schema refuses the field), so this prose
// exists for the contract; it states the fallback's blast radius.
func (*CIDR) BlastRadius(r *spec.Resource, s *spec.Spec) string {
	return "the subnet is carved from a shared pool: a worktree whose slot falls past the pool's last block shares the whole pool with every other fallen-back worktree, and nothing reallocates it when a slot later frees"
}
