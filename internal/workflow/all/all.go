// Package all imports every workflow so they register themselves.
package all

import (
	_ "github.com/solcyber/gh-dillodash/internal/workflow/maintenancewindow"
	_ "github.com/solcyber/gh-dillodash/internal/workflow/releasepr"
	_ "github.com/solcyber/gh-dillodash/internal/workflow/terraformapprove"
	_ "github.com/solcyber/gh-dillodash/internal/workflow/waitingruns"
)
