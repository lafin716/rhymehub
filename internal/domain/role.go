package domain

import "fmt"

// Role is a user role. Ordering: reporter < developer < maintainer < admin.
type Role string

const (
	RoleReporter   Role = "reporter"
	RoleDeveloper  Role = "developer"
	RoleMaintainer Role = "maintainer"
	RoleAdmin      Role = "admin"
)

// rank returns the position in the ordering, or 0 for unknown roles.
func (r Role) rank() int {
	switch r {
	case RoleReporter:
		return 1
	case RoleDeveloper:
		return 2
	case RoleMaintainer:
		return 3
	case RoleAdmin:
		return 4
	}
	return 0
}

// Valid reports whether r is one of the four known roles.
func (r Role) Valid() bool {
	return r.rank() > 0
}

// AtLeast reports whether r is at least min in the role ordering. Unknown roles never qualify.
func (r Role) AtLeast(min Role) bool {
	if !r.Valid() || !min.Valid() {
		return false
	}
	return r.rank() >= min.rank()
}

// ParseRole validates a role string.
func ParseRole(s string) (Role, error) {
	r := Role(s)
	if !r.Valid() {
		return "", fmt.Errorf("%w: unknown role %q", ErrInvalid, s)
	}
	return r, nil
}
