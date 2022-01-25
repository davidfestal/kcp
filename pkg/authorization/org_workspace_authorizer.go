package authorization

import (
	"k8s.io/apiserver/pkg/authorization/authorizer"
	clientgoinformers "k8s.io/client-go/informers"
	"k8s.io/client-go/tools/cache"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"
	rbacauthorizer "k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"
)

func NewOrgWorkspaceAuthorizer(informers clientgoinformers.SharedInformerFactory) (authorizer.Authorizer, authorizer.RuleResolver) {
	// TODO: currently hardcoded, change to dynamically loading the org workspace
	scope := cache.NewScope("admin")

	a := rbac.New(&rbacauthorizer.RoleGetter{Lister: informers.Rbac().V1().Roles().Lister().Scoped(scope)},
		&rbacauthorizer.RoleBindingLister{Lister: informers.Rbac().V1().RoleBindings().Lister().Scoped(scope)},
		&rbacauthorizer.ClusterRoleGetter{Lister: informers.Rbac().V1().ClusterRoles().Lister().Scoped(scope)},
		&rbacauthorizer.ClusterRoleBindingLister{Lister: informers.Rbac().V1().ClusterRoleBindings().Lister().Scoped(scope)})

	return a, a
}
