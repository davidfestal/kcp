package authorization

import (
	"context"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	clientgoinformers "k8s.io/client-go/informers"
	rbacv1listers "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/client-go/rest"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"
)

type LocalAuthorizer struct {
	roleLister               rbacv1listers.RoleLister
	roleBindingLister        rbacv1listers.RoleBindingLister
	clusterRoleBindingLister rbacv1listers.ClusterRoleBindingLister
	clusterRoleLister        rbacv1listers.ClusterRoleLister
}

func NewLocalAuthorizer(versionedInformers clientgoinformers.SharedInformerFactory) (authorizer.Authorizer, authorizer.RuleResolver) {
	a := &LocalAuthorizer{
		roleLister:               versionedInformers.Rbac().V1().Roles().Lister(),
		roleBindingLister:        versionedInformers.Rbac().V1().RoleBindings().Lister(),
		clusterRoleLister:        versionedInformers.Rbac().V1().ClusterRoles().Lister(),
		clusterRoleBindingLister: versionedInformers.Rbac().V1().ClusterRoleBindings().Lister(),
	}
	return a, a
}

func (a *LocalAuthorizer) RulesFor(user user.Info, namespace string) ([]authorizer.ResourceRuleInfo, []authorizer.NonResourceRuleInfo, bool, error) {
	// TODO: wire context in RulesFor interface
	panic("implement me")
}

func (a *LocalAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	reqScope := rest.ScopeFrom(ctx)
	if reqScope == nil || reqScope.Name() == "" {
		return authorizer.DecisionNoOpinion, "", nil
	}

	scopedAuth := rbac.New(
		&rbac.RoleGetter{Lister: a.roleLister.Scoped(reqScope)},
		&rbac.RoleBindingLister{Lister: a.roleBindingLister.Scoped(reqScope)},
		&rbac.ClusterRoleGetter{Lister: a.clusterRoleLister.Scoped(reqScope)},
		&rbac.ClusterRoleBindingLister{Lister: a.clusterRoleBindingLister.Scoped(reqScope)},
	)

	return scopedAuth.Authorize(ctx, attr)
}
