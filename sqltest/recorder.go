package sqltest

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"sync"
)

// Op names a recorded driver call.
type Op string

const (
	OpExec     Op = "exec"
	OpQuery    Op = "query"
	OpPrepare  Op = "prepare"
	OpBegin    Op = "begin"
	OpCommit   Op = "commit"
	OpRollback Op = "rollback"
)

// Call is one recorded driver call. SQL and Args are set for exec, query,
// and prepare; TxOptions for begin.
type Call struct {
	Op        Op
	SQL       string
	Args      []any
	TxOptions driver.TxOptions
}

// Response scripts the outcome of the next exec or query call: an error, or
// the affected count (exec) or the columns and rows (query). An exec
// response has no columns or rows and a query response no affected
// count; every row is as wide as Columns and contains driver.Value types only,
// as a real driver returns them. Prepare, begin, commit, and rollback do not
// consume responses; their failures are set on the Recorder directly.
type Response struct {
	Err      error
	Affected int64
	Columns  []string
	Rows     [][]driver.Value
}

// Recorder is the shared state behind every connection a connector hands
// out: the response queue, the call log, and the failure switches.
type Recorder struct {
	mu        sync.Mutex
	responses []Response
	calls     []Call
	rowsOpen  int
	rowsClose int

	// FailPrepare, when set, decides whether a prepare of the given text
	// fails; nil means every prepare succeeds.
	FailPrepare func(query string) error
	// FailBegin, FailCommit, FailRollback, FailPing inject failures into the
	// transaction and ping calls.
	FailBegin, FailCommit, FailRollback, FailPing error
}

// Queue appends responses for later exec and query calls.
func (r *Recorder) Queue(responses ...Response) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responses = append(r.responses, responses...)
}

// Calls returns a copy of the call log in order.
func (r *Recorder) Calls() []Call {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Call, len(r.calls))
	copy(out, r.calls)
	return out
}

// SQL returns the statement texts recorded for op, in order.
func (r *Recorder) SQL(op Op) []string {
	var out []string
	for _, c := range r.Calls() {
		if c.Op == op {
			out = append(out, c.SQL)
		}
	}
	return out
}

// Ops returns the sequence of operations recorded.
func (r *Recorder) Ops() []Op {
	calls := r.Calls()
	out := make([]Op, len(calls))
	for i, c := range calls {
		out[i] = c.Op
	}
	return out
}

// Pending reports how many scripted responses remain unconsumed; a test
// asserts zero to prove every scripted call happened.
func (r *Recorder) Pending() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.responses)
}

// RowsLeaked reports row sets opened and never closed.
func (r *Recorder) RowsLeaked() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rowsOpen - r.rowsClose
}

func (r *Recorder) record(c Call) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
}

// next records the call, checks the arguments against the statement's
// placeholders, and pops the next response, rejecting an empty queue and a
// response of the wrong shape. The call is recorded even when it fails, so
// the log shows what ran.
func (r *Recorder) next(op Op, query string, args []driver.NamedValue) (Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	values := make([]any, len(args))
	for i, a := range args {
		values[i] = a.Value
	}
	r.calls = append(r.calls, Call{Op: op, SQL: query, Args: values})
	if want := placeholders(query); want != len(args) {
		return Response{}, fmt.Errorf("%w: %s expects %d, got %d", ErrArguments, op, want, len(args))
	}
	if len(r.responses) == 0 {
		return Response{}, fmt.Errorf("%w: %s %q", ErrUnscripted, op, query)
	}
	resp := r.responses[0]
	r.responses = r.responses[1:]
	if resp.Err != nil {
		return resp, nil
	}
	if err := resp.fits(op); err != nil {
		return Response{}, fmt.Errorf("%w: %s %q: %w", ErrScript, op, query, err)
	}
	return resp, nil
}

// fits checks the response's shape against the call that consumed it.
func (resp Response) fits(op Op) error {
	switch op {
	case OpExec:
		if resp.Columns != nil || resp.Rows != nil {
			return errors.New("a query response was scripted for an exec")
		}
	case OpQuery:
		if resp.Affected != 0 {
			return errors.New("an exec response was scripted for a query")
		}
		for i, row := range resp.Rows {
			if len(row) != len(resp.Columns) {
				return fmt.Errorf("row %d has %d values for %d columns", i, len(row), len(resp.Columns))
			}
			for j, v := range row {
				if !driver.IsValue(v) {
					return fmt.Errorf("row %d column %s is %T, not a driver.Value", i, resp.Columns[j], v)
				}
			}
		}
	}
	return nil
}

var placeholder = regexp.MustCompile(`\$([0-9]+)`)

// placeholders returns the argument count a statement binds: the highest $N
// in its text, the stub dialect's placeholder form.
func placeholders(query string) int {
	max := 0
	for _, m := range placeholder.FindAllStringSubmatch(query, -1) {
		if n, err := strconv.Atoi(m[1]); err == nil && n > max {
			max = n
		}
	}
	return max
}
