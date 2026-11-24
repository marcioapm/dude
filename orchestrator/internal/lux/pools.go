package lux

import "slices"

// DefaultPool mirrors lux's resolvePool, so a Run that names no pool lands
// where lux puts it: the tenant's marked default, else the platform's, else
// the pool named "default", the tenant's before the platform's. A key sees
// only its tenant's pools and the platform's; Platform tells them apart.
func DefaultPool(pools []Pool) (Pool, bool) {
	for _, match := range []func(Pool) bool{
		func(p Pool) bool { return !p.Platform && p.IsDefault },
		func(p Pool) bool { return p.Platform && p.IsDefault },
		func(p Pool) bool { return !p.Platform && p.Name == "default" },
		func(p Pool) bool { return p.Platform && p.Name == "default" },
	} {
		if i := slices.IndexFunc(pools, match); i >= 0 {
			return pools[i], true
		}
	}
	return Pool{}, false
}
