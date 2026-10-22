package worker

import (
	"crypto/rand"
	"fmt"
)

const workerStateDomain = "relayweave-worker-v3"
const workerMetaDomain = "relayweave-meta-v3"

func (a *Agent) sealWorkerPayload(plain []byte, domain string) ([]byte, error) {
	nonce := make([]byte, a.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.aead.Seal(nonce, nonce, plain, []byte(domain)), nil
}

func (a *Agent) openWorkerPayload(raw []byte, domain, legacy string) ([]byte, bool, error) {
	n := a.aead.NonceSize()
	if len(raw) <= n {
		return nil, false, fmt.Errorf("worker_payload_corrupt")
	}
	if plain, err := a.aead.Open(nil, raw[:n], raw[n:], []byte(domain)); err == nil {
		return plain, false, nil
	}
	plain, err := a.aead.Open(nil, raw[:n], raw[n:], []byte(legacy))
	if err != nil {
		return nil, false, fmt.Errorf("worker_payload_corrupt")
	}
	return plain, true, nil
}
