package compute

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

type captured struct {
	s               Snapshot
	hash            string
	at              time.Duration
	epoch, sequence int64
}

// Cache is local authority for freshness. Neither dispatcher nor compute wall
// timestamps can make old observations fresh. Restart produces a new boot ID.
type Cache struct {
	mu         sync.Mutex
	Boot       string
	secret     [32]byte
	Now        func() (time.Duration, error)
	next       int64
	items      map[string]captured
	keys       map[string]ed25519.PublicKey
	OwnerEpoch int64
}

func NewCache(keys map[string]ed25519.PublicKey) *Cache {
	c := &Cache{Boot: newID(), Now: bootElapsed, items: map[string]captured{}}
	rand.Read(c.secret[:])
	c.SetKeys(keys)
	return c
}
func (c *Cache) Opaque(value string) string {
	mac := hmac.New(sha256.New, c.secret[:])
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}
func (c *Cache) SetKeys(keys map[string]ed25519.PublicKey) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.keys = make(map[string]ed25519.PublicKey)
	// Called only when the trusted key file changes. Revocation invalidates
	// existing plans as well as future signature checks.
	c.items = map[string]captured{}
	if len(keys) > 2 {
		return
	}
	for id, key := range keys {
		if opaque(id) && len(key) == ed25519.PublicKeySize {
			c.keys[id] = append(ed25519.PublicKey(nil), key...)
		}
	}
}
func (c *Cache) Capture(s Snapshot) (Snapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) >= 32 {
		if _, exists := c.items[s.Policy]; !exists {
			return s, ErrBudget
		}
	}
	now, e := c.Now()
	if e != nil {
		return s, e
	}
	c.next++
	s.Boot, s.Sequence = c.Boot, c.next
	if e = s.Validate(); e != nil {
		return s, e
	}
	h, e := Hash(s)
	if e != nil {
		return s, e
	}
	stored := s
	stored.Groups = append([]Group(nil), s.Groups...)
	c.items[s.Policy] = captured{s: stored, hash: h, at: now}
	return s, nil
}

func (c *Cache) SetEpoch(epoch int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if epoch > c.OwnerEpoch {
		c.OwnerEpoch = epoch
		for key, v := range c.items {
			v.epoch, v.sequence = 0, 0
			c.items[key] = v
		}
	}
}

func (c *Cache) Accept(ctx context.Context, r Recommendation, generation int64, routeSet string) ([]uint32, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, exists := c.items[r.Snapshot.Policy]
	now, e := c.Now()
	if e != nil || !exists || now < v.at || now-v.at > 30*time.Second || r.Snapshot.Boot != c.Boot || r.Snapshot.Sequence != v.s.Sequence || r.Snapshot.Generation != generation || r.Snapshot.RouteSet != routeSet || r.SnapshotDigest != v.hash || r.OwnerEpoch != c.OwnerEpoch || r.OwnerEpoch < 1 || r.Sequence <= v.sequence {
		return nil, ErrStale
	}
	h, e := Hash(r.Snapshot)
	if e != nil || h != v.hash {
		return nil, ErrInvalid
	}
	if e = CheckSignature(r, c.keys[r.KeyID]); e != nil {
		return nil, e
	}
	if e = Verify(ctx, v.s, r.Candidate); e != nil {
		return nil, e
	}
	if r.BlendPPM == 0 || r.BlendPPM > 250000 {
		return nil, ErrInvalid
	}
	weights := make([]uint32, len(v.s.Groups))
	var sum uint32
	for i, g := range v.s.Groups {
		weights[i] = uint32((uint64(g.Baseline)*uint64(PPM-r.BlendPPM) + uint64(r.Candidate.Weights[i])*uint64(r.BlendPPM)) / PPM)
		sum += weights[i]
	}
	for i := 0; sum < PPM; i = (i + 1) % len(weights) {
		weights[i]++
		sum++
	}
	v.sequence, v.epoch = r.Sequence, r.OwnerEpoch
	c.items[r.Snapshot.Policy] = v
	return weights, nil
}

func (c *Cache) Fresh(policy string, sequence int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[policy]
	now, e := c.Now()
	return ok && e == nil && v.s.Sequence == sequence && now >= v.at && now-v.at <= 30*time.Second
}

func (c *Cache) Deadline(policy string, sequence int64) (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[policy]
	return v.at + 30*time.Second, ok && v.s.Sequence == sequence
}
func Elapsed() (time.Duration, error) { return bootElapsed() }
func (c *Cache) Clear()               { c.mu.Lock(); defer c.mu.Unlock(); c.items = map[string]captured{} }
