package auth

// Sistemdeki altı rol; users.role CHECK kısıtıyla birebir aynı değerler.
const (
	RoleStudent       = "student"
	RoleInstructor    = "instructor"
	RoleAdvisor       = "advisor"
	RoleDeptSecretary = "dept_secretary"
	RoleRegistrar     = "registrar"
	RoleReporting     = "reporting"
)

var AllRoles = []string{
	RoleStudent, RoleInstructor, RoleAdvisor,
	RoleDeptSecretary, RoleRegistrar, RoleReporting,
}
