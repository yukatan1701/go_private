package extra

import "hash/fnv"

type stringHash uint64
type stackHash stringHash
type frameHash stringHash
type funcHash stringHash

func hashString64(s string) (stringHash, error) {
	h := fnv.New64a()
	_, err := h.Write([]byte(s))
	if err != nil {
		return stringHash(0), err
	}
	return stringHash(h.Sum64()), nil
}
