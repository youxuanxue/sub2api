package domain

// UserListFilters contains filter options for listing users.
type UserListFilters struct {
	Status               string
	Role                 string
	Search               string
	GroupName            string
	APIKeyGroupID        int64
	Attributes           map[int64]string
	IncludeSubscriptions *bool
	IncludeDeleted       bool
}
