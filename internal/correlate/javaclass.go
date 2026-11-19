package correlate

import (
	"iormesh/internal/agentspool"
	"iormesh/internal/schema"
)

// methodKey identifies a method table row (task fe4): a frame of one
// class-table class. The same frame text from two JARs is two rows, so
// every method id joins to exactly one JAR; class 0 is a frame whose
// CLASS record never arrived.
type methodKey struct {
	frame string
	class uint64
}

// rename is one later name of a Java thread (task fe4): the thread's
// brackets that exit after after carry it, those before keep the name
// in force then.
type rename struct {
	after uint64 // CLOCK_MONOTONIC
	name  string
}

// class returns the trace's class id for the agent's class id aid,
// writing its class table row the first time a frame names it; 0 when
// the agent described no class (aid 0) or its CLASS record is not here
// (dropped). Rows are keyed by their content, not by JVM: the same class
// from the same JAR in two JVMs is one id. The code source is cleaned
// again here, as HTTP headers are redacted twice: a ring is untrusted
// input.
func (j *JavaJoin) class(v *jvm, aid uint64) (uint64, error) {
	if id, ok := v.classIDs[aid]; ok {
		return id, nil
	}
	c, ok := v.classes[aid]
	if aid == 0 || !ok {
		return 0, nil
	}
	row := schema.Class{SchemaVersion: schema.Version, Class: c.Name, Loader: c.Loader,
		CodeSource: schema.CleanCodeSource(c.CodeSource)}
	id, ok := j.classes[row]
	if !ok {
		id = uint64(len(j.classes) + 1)
		row.ClassID = id
		if err := j.cfg.Tables.Class(row); err != nil {
			return 0, err
		}
		row.ClassID = 0
		j.classes[row] = id
	}
	v.classIDs[aid] = id
	return id, nil
}

// rename files a later THREAD record of thread first (its first record)
// carrying name. The agent writes it at the thread's first bracket enter
// after the rename, so the thread's brackets read before it — up to the
// latest exit among them still held — keep the earlier name; a rename to
// the name already in force changes nothing.
func (v *jvm) rename(first agentspool.Thread, name string) {
	list := v.renames[first.ID]
	current := first.Name
	if len(list) > 0 {
		current = list[len(list)-1].name
	}
	if name == current {
		return
	}
	v.renames[first.ID] = append(list, rename{after: v.lastExit(first), name: name})
}

// lastExit is the latest exit among the brackets held for thread th, 0
// when none is: a platform thread's are all on its tid; a virtual
// thread's (or one whose tid is unknown) may be on any carrier, so every
// list is searched — a rename is rare, the search bounded by what is
// held.
func (v *jvm) lastExit(th agentspool.Thread) uint64 {
	if !th.Virtual && th.KTID > 0 {
		return lastExitIn(v.brackets[th.KTID], th.ID)
	}
	var last uint64
	for _, bl := range v.brackets {
		last = max(last, lastExitIn(bl, th.ID))
	}
	return last
}

// lastExitIn is the latest exit of thread id's brackets in bl (exit
// order, so the first match from the end), 0 for none.
func lastExitIn(bl *bracketList, id uint64) uint64 {
	if bl == nil {
		return 0
	}
	for i := len(bl.list) - 1; i >= 0; i-- {
		if bl.list[i].ThreadID == id {
			return bl.list[i].ExitMono
		}
	}
	return 0
}

// nameAt is thread th's name for a record that exited at mono: the
// latest rename it exited after, else the first name.
func (v *jvm) nameAt(th agentspool.Thread, mono uint64) string {
	name := th.Name
	for _, r := range v.renames[th.ID] {
		if mono > r.after {
			name = r.name
		}
	}
	return name
}
