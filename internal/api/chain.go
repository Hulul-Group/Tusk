package api

import "net/http"

// chain is a tiny port of an express-validator 6 ValidationChain for one
// body/param/query field. Semantics that matter for Kutt parity:
//   - optional() is re-evaluated before EVERY step with the current value
//     (context-runner-impl.js getData({requiredOnly:true})), so a trim() that
//     empties an optional({checkFalsy}) field skips the remaining steps;
//   - standard validators see utils.toString(value);
//   - a step without withMessage uses the field message (body(field, msg)) or
//     "Invalid value";
//   - only the first error matters (helpers.verify returns errors[0].msg).
type chain struct {
	b        body
	key      string
	v        any
	opt      int // 0 = required-ish, 1 = optional({nullable}), 2 = optional({nullable, checkFalsy})
	fieldMsg string
	err      string
}

const (
	optNone = iota
	optNullable
	optFalsy
)

func bodyField(b body, key, fieldMsg string, opt int) *chain {
	return &chain{b: b, key: key, v: b[key], opt: opt, fieldMsg: fieldMsg}
}

func valueField(key string, v any, fieldMsg string, opt int) *chain {
	return &chain{key: key, v: v, opt: opt, fieldMsg: fieldMsg}
}

func (c *chain) active() bool {
	if c.err != "" {
		return false
	}
	switch c.opt {
	case optNullable:
		return c.v != nil
	case optFalsy:
		return truthy(c.v)
	}
	return true
}

func (c *chain) fail(msg string) {
	if c.err != "" {
		return
	}
	if msg == "" {
		msg = c.fieldMsg
	}
	if msg == "" {
		msg = "Invalid value"
	}
	c.err = msg
}

func (c *chain) check(ok func(v any) bool, msg string) *chain {
	if c.active() && !ok(c.v) {
		c.fail(msg)
	}
	return c
}

func (c *chain) exists(msg string) *chain { return c.check(truthy, msg) }
func (c *chain) isString(msg string) *chain {
	return c.check(func(v any) bool { return isString(v) }, msg)
}
func (c *chain) length(min, max int, msg string) *chain {
	return c.check(func(v any) bool {
		n := jsLen(jsString(v))
		return n >= min && (max < 0 || n <= max)
	}, msg)
}
func (c *chain) str(fn func(string) bool, msg string) *chain {
	return c.check(func(v any) bool { return fn(jsString(v)) }, msg)
}
func (c *chain) isEmail(msg string) *chain { return c.str(isEmail, msg) }
func (c *chain) isNumeric(msg string) *chain {
	return c.str(isNumeric, msg)
}
func (c *chain) isBoolean(msg string) *chain { return c.check(isBooleanStrict, msg) }

// sanitize runs a custom sanitizer on the raw value.
func (c *chain) sanitize(fn func(v any) any) *chain {
	if c.active() {
		c.v = fn(c.v)
	}
	return c
}

// trim is the standard trim() sanitizer (operates on toString(value)).
func (c *chain) trim() *chain {
	return c.sanitize(func(v any) any { return jsTrim(jsString(v)) })
}

// customStr is a custom sanitizer/validator over a JS value that the Kutt
// code treats as a string (after trim it always is).
func (c *chain) mapStr(fn func(string) string) *chain {
	return c.sanitize(func(v any) any {
		if s, ok := v.(string); ok {
			return fn(s)
		}
		return fn(jsString(v))
	})
}

// done writes the sanitized value back (req.body mutation) and returns the error.
func (c *chain) done() string {
	if c.b != nil {
		if _, had := c.b[c.key]; had || c.v != nil {
			c.b[c.key] = c.v
		}
	}
	return c.err
}

// verify returns the first error of the chains as a 400 (helpers.verify).
func verify(chains ...*chain) error {
	for _, c := range chains {
		if e := c.done(); e != "" {
			return errBody(http.StatusBadRequest, e)
		}
	}
	return nil
}
