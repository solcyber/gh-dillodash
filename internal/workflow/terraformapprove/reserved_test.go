package terraformapprove

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/solcyber/gh-dillodash/internal/clock"
	"github.com/solcyber/gh-dillodash/internal/config"
)

// A harmless plan to a reserved stack, inside the reserved span: a plain
// /approve would be refused, so a offers to wait (the default) or the
// outside-window flag with a reason. Outside the span, or for a stack
// that is not reserved, nothing changes.
func TestReservedStackOffersOutsideWindow(t *testing.T) {
	cfg := testCfg()
	cfg.MaintenanceWindow.Reserved = &config.ReservedWindow{From: clock.HM{H: 21, M: 50}, To: clock.HM{H: 23, M: 15}, Stacks: []string{"prod/*"}}
	ny, _ := time.LoadLocation("America/New_York")
	setNow := func(h, m int) {
		old := clock.Now
		clock.Now = func() time.Time { return time.Date(2026, 10, 28, h, m, 0, 0, ny) }
		t.Cleanup(func() { clock.Now = old })
	}
	harmless := windowed("non-disruptive", 0, false)

	setNow(22, 0)
	app, _ := approve(t, cfg, harmless, script{picks: []int{1}, prompts: []string{reason}, confirms: []bool{true}})
	check(t, "inside: the menu says a plain approve is refused", len(app.Menus) == 1 && strings.Contains(app.Menus[0][0], "REFUSED until 23:15"))
	check(t, "inside: waiting is the first entry", len(app.Menus) == 1 && strings.HasPrefix(app.Menus[0][1], "cancel"))
	check(t, "inside, now: --outside-window with the reason", eq(posted(app), one(3622, fmt.Sprintf(`/approve %s --outside-window="%s"`, idA, reason))))

	app, _ = approve(t, cfg, harmless, script{picks: []int{0}})
	check(t, "inside, wait: nothing posted", len(app.Fake.CommentsPosted) == 0)

	setNow(20, 0)
	app, _ = approve(t, cfg, harmless, script{confirms: []bool{true}})
	check(t, "outside the span: no menu, plain /approve", len(app.Menus) == 0 && eq(posted(app), one(3622, "/approve "+idA)))

	setNow(22, 0)
	cfg.MaintenanceWindow.Reserved.Stacks = []string{"staging/*"}
	app, _ = approve(t, cfg, harmless, script{confirms: []bool{true}})
	check(t, "a stack that is not reserved: plain /approve", len(app.Menus) == 0 && eq(posted(app), one(3622, "/approve "+idA)))
}
