package compute

import "proxyplatform/internal/identity"

func saveRenewedBundle(path string, bundle identity.Bundle) error { return identity.Save(path, bundle) }
