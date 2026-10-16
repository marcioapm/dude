package redact

// matcher finds every occurrence of every pattern in one pass over the
// text (Aho-Corasick over bytes). Patterns are deduplicated by FromList, so
// at most one ends at a trie node. Edges are stored compactly (CSR, sorted
// by byte) because a long value's forms make long single-child chains; the
// root's are a full table, as most bytes of ordinary text leave it at once.
type matcher struct {
	root   [256]int32 // the root's goto: the child, or 0 (the root)
	off    []int32    // node n's edges are edgeB/edgeT[off[n]:off[n+1]]
	edgeB  []byte
	edgeT  []int32
	fail   []int32
	out    []int32 // the pattern ending at the node, or -1
	dict   []int32 // the nearest node on the fail chain with an out, or -1
	lens   []int
	maxLen int
}

func newMatcher(pats []pattern) *matcher {
	type edge struct {
		parent int32
		b      byte
	}
	children := map[edge]int32{}
	parent, via := []int32{-1}, []byte{0}
	out := []int32{-1}
	m := &matcher{lens: make([]int, len(pats))}
	for i, p := range pats {
		n := int32(0)
		for j := 0; j < len(p.from); j++ {
			e := edge{n, p.from[j]}
			c, ok := children[e]
			if !ok {
				c = int32(len(parent))
				children[e] = c
				parent, via, out = append(parent, n), append(via, p.from[j]), append(out, -1)
			}
			n = c
		}
		out[n] = int32(i)
		m.lens[i] = len(p.from)
		m.maxLen = max(m.maxLen, len(p.from))
	}
	nodes := len(parent)
	m.out = out
	// CSR edges, each node's sorted by byte: counted, placed, then each
	// node's run sorted (insertion sort; fan-out is small).
	m.off = make([]int32, nodes+1)
	for c := 1; c < nodes; c++ {
		m.off[parent[c]+1]++
	}
	for n := 0; n < nodes; n++ {
		m.off[n+1] += m.off[n]
	}
	m.edgeB, m.edgeT = make([]byte, nodes-1), make([]int32, nodes-1)
	fill := append([]int32(nil), m.off[:nodes]...)
	for c := 1; c < nodes; c++ {
		at := fill[parent[c]]
		fill[parent[c]]++
		m.edgeB[at], m.edgeT[at] = via[c], int32(c)
	}
	for n := 0; n < nodes; n++ {
		lo, hi := m.off[n], m.off[n+1]
		for i := lo + 1; i < hi; i++ {
			for j := i; j > lo && m.edgeB[j] < m.edgeB[j-1]; j-- {
				m.edgeB[j], m.edgeB[j-1] = m.edgeB[j-1], m.edgeB[j]
				m.edgeT[j], m.edgeT[j-1] = m.edgeT[j-1], m.edgeT[j]
			}
		}
	}
	for i := m.off[0]; i < m.off[1]; i++ {
		m.root[m.edgeB[i]] = m.edgeT[i]
	}
	// Fail and dictionary links, breadth first: each from shallower nodes.
	m.fail, m.dict = make([]int32, nodes), make([]int32, nodes)
	m.dict[0] = -1
	queue := make([]int32, 0, nodes)
	for i := m.off[0]; i < m.off[1]; i++ {
		c := m.edgeT[i]
		m.dict[c] = -1
		queue = append(queue, c)
	}
	for q := 0; q < len(queue); q++ {
		u := queue[q]
		for i := m.off[u]; i < m.off[u+1]; i++ {
			v := m.edgeT[i]
			f := m.step(m.fail[u], m.edgeB[i])
			m.fail[v] = f
			if m.out[f] >= 0 {
				m.dict[v] = f
			} else {
				m.dict[v] = m.dict[f]
			}
			queue = append(queue, v)
		}
	}
	return m
}

// child is n's child by b, or -1.
func (m *matcher) child(n int32, b byte) int32 {
	lo, hi := m.off[n], m.off[n+1]
	for lo < hi {
		mid := (lo + hi) / 2
		switch {
		case m.edgeB[mid] == b:
			return m.edgeT[mid]
		case m.edgeB[mid] < b:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return -1
}

// step is the node after n reads b.
func (m *matcher) step(n int32, b byte) int32 {
	for n != 0 {
		if c := m.child(n, b); c >= 0 {
			return c
		}
		n = m.fail[n]
	}
	return m.root[b]
}

// scan calls emit for every match in s, in order of end (longest first at
// one end). With limit, it stops before reading s[*limit], which emit may
// lower.
func (m *matcher) scan(s string, limit *int, emit func(span)) {
	n := int32(0)
	for i := 0; i < len(s); i++ {
		if limit != nil && i >= *limit {
			return
		}
		if n == 0 {
			// At the root most bytes start nothing: skip them without a step.
			for i < len(s) && m.root[s[i]] == 0 {
				i++
			}
			if i == len(s) || limit != nil && i >= *limit {
				return
			}
			n = m.root[s[i]]
		} else {
			n = m.step(n, s[i])
		}
		o := n
		if m.out[o] < 0 {
			o = m.dict[o]
		}
		for ; o >= 0; o = m.dict[o] {
			p := int(m.out[o])
			emit(span{i + 1 - m.lens[p], i + 1, p})
		}
	}
}
