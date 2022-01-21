package authorization

import (
	"context"
	"github.com/kcp-dev/kcp/pkg/apis/tenancy/v1alpha1"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	clientgoinformers "k8s.io/client-go/informers"
	rbacv1listers "k8s.io/client-go/listers/rbac/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/plugin/pkg/auth/authorizer/rbac"
	"strings"
)

func NewWorkspaceAuthorizer(versionedInformers clientgoinformers.SharedInformerFactory, delegate authorizer.Authorizer) authorizer.Authorizer {
	return &OrgWorkspaceAuthorizer{
		roleLister:               versionedInformers.Rbac().V1().Roles().Lister(),
		roleBindingLister:        versionedInformers.Rbac().V1().RoleBindings().Lister(),
		clusterRoleLister:        versionedInformers.Rbac().V1().ClusterRoles().Lister(),
		clusterRoleBindingLister: versionedInformers.Rbac().V1().ClusterRoleBindings().Lister(),
		delegate:                 delegate,
	}
}

type OrgWorkspaceAuthorizer struct {
	roleLister               rbacv1listers.RoleLister
	roleBindingLister        rbacv1listers.RoleBindingLister
	clusterRoleBindingLister rbacv1listers.ClusterRoleBindingLister
	clusterRoleLister        rbacv1listers.ClusterRoleLister

	//delegate func(scope rest.Scope) authorizer.Authorizer // union of local and bootstrap authorizer
	delegate authorizer.Authorizer // union of local and bootstrap authorizer
}

func (a *OrgWorkspaceAuthorizer) Authorize(ctx context.Context, attr authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	reqScope := rest.ScopeFrom(ctx)
	if reqScope == nil || reqScope.Name() == "" {
		return authorizer.DecisionNoOpinion, "", nil
	}

	// future: when we have org workspaces, after Steve's proxy PR merged
	// Probably like system:org-replica:reqScope.Name().
	// TODO: not completely true: we will have a (partial) replica of the org workspace on this shard, which holds the necessary
	//       RBAC objects to do this very authorization work.
	// For now, "admin" is our org workspace:
	orgWorkspace := "admin"

	orgScope := cache.NewScope(orgWorkspace)
	orgAuthorizer := rbac.New(
		&rbac.RoleGetter{Lister: a.roleLister.Scoped(orgScope)},
		&rbac.RoleBindingLister{Lister: a.roleBindingLister.Scoped(orgScope)},
		&rbac.ClusterRoleGetter{Lister: a.clusterRoleLister.Scoped(orgScope)},
		&rbac.ClusterRoleBindingLister{Lister: a.clusterRoleBindingLister.Scoped(orgScope)},
	)

	verbToGroupMembership := map[string]string{
		"admin":   "cluster-admin",
		"edit":    "system:kcp:workspace:edit",
		"view":    "system:kcp:workspace:view",
		"default": "",
	}

	extraGroups := []string{}
	var (
		errList    []error
		reasonList []string
	)
	for verb, group := range verbToGroupMembership {
		workspaceAttr := authorizer.AttributesRecord{
			User:        attr.GetUser(),
			Verb:        verb,
			APIGroup:    v1alpha1.SchemeGroupVersion.Group,
			APIVersion:  v1alpha1.SchemeGroupVersion.Version,
			Resource:    "workspaces",
			Subresource: "content", Name: reqScope.Name(), // TODO: parse and remove org prefix as soon as Steve's proxy PR merges
			ResourceRequest: true,
		}

		dec, reason, err := orgAuthorizer.Authorize(ctx, workspaceAttr)
		if err != nil {
			errList = append(errList, err)
			reasonList = append(reasonList, reason)
			continue
		}
		if dec == authorizer.DecisionAllow {
			extraGroups = append(extraGroups, group)
		}
	}
	if len(errList) > 0 {
		return authorizer.DecisionNoOpinion, strings.Join(reasonList, "\n"), utilerrors.NewAggregate(errList)
	}
	if len(extraGroups) == 0 {
		return authorizer.DecisionNoOpinion, "workspace access not permitted", nil
	}

	klog.Infof("adding groups: %v", extraGroups)

	attrsWithExtraGroups := authorizer.AttributesRecord{
		User: &user.DefaultInfo{
			Name:   attr.GetUser().GetName(),
			UID:    attr.GetUser().GetUID(),
			Groups: append(attr.GetUser().GetGroups(), extraGroups...),
			Extra:  attr.GetUser().GetExtra(),
		},
		Verb:            attr.GetVerb(),
		Namespace:       attr.GetNamespace(),
		APIGroup:        attr.GetAPIGroup(),
		APIVersion:      attr.GetAPIVersion(),
		Resource:        attr.GetResource(),
		Subresource:     attr.GetSubresource(),
		Name:            attr.GetName(),
		ResourceRequest: attr.IsResourceRequest(),
		Path:            attr.GetPath(),
	}

	return a.delegate.Authorize(ctx, attrsWithExtraGroups)
}

/**
// BuildAuthorizer constructs the authorizer
func BuildAuthorizer(s *options.ServerRunOptions, versionedInformers clientgoinformers.SharedInformerFactory) (authorizer.Authorizer, authorizer.RuleResolver, error) {
	localWorkspaceRbacAuthorizer := rbac.New(
		&rbac.RoleGetter{Lister: versionedInformers.Rbac().V1().Roles().Lister()},
		&rbac.RoleBindingLister{Lister: versionedInformers.Rbac().V1().RoleBindings().Lister()},
		&rbac.ClusterRoleGetter{Lister: versionedInformers.Rbac().V1().ClusterRoles().Lister()},
		InjectKcpRoleBindings{&rbac.ClusterRoleBindingLister{Lister: versionedInformers.Rbac().V1().ClusterRoleBindings().Lister()}},
	)
	bootstrapPolicyAuthorizer := ...
	orAuthorizers = union.New(bootstrapPolicyAuthorizer, localWorkspaceRbacAuthorizer
	orgWorkspaceAuthz := OrgWorkspaceAuthorizer{..., delegate: orAuthorizers}

	return orgWorkspaceAuthz, orgWorkspaceAuthz, nil
}

type OrgWorkspaceAuthorizer struct {
	roleGetter               rbacv1.RolesGetter
	roleBindingLister        rbaclisterv1.RoleBindingLister
	clusterRoleGetter        rbacv1.ClusterRolesGetter
	clusterRoleBindingLister rbaclisterv1.ClusterRoleBindingLister

	delegate authorizer.Authorizer // union of local and bootstrap authorizer
}

func (a *OrgWorkspaceAuthorizer) Authorize(ctx context.Context, a authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	cluster, err := genericapirequest.ValidClusterFrom(ctx)
	if err != nil {
		return ..., nil, err
	}

	// future: when we have org workspace, after Steve's proxy PR merged
	// workspace := workspaceGetter.Get(cluster)
	// org := workspace.Org
	// orgWorkspace := workspaceGetter.Get(org)
	// TODO: not completely true: we will have a (partial) replica of the org workspace on this shard, which holds the necessary
	//       RBAC objects to do this very authorization work.
	//
	// For now, "admin" is our org workspace:
	orgWorkspace := "admin"

	scopedRbacAuthorizer := rbac.New(
		a.roleGetter.Scoped(scoped.WithCluster(orgWorkspace)),
		a.roleBindingLister.Scoped(...),
	...
)

	verbToGroupMembership := map[string]string{
		"admin":   "system:masters",
		"editor":  "system:kcp:editor", // TODO: add to bootstrap policy from kcp
		"reader":  "system:kcp:reader", // TODO: add to bootstrap policy from kcp
		"default": "",                  // "system:authenticated", // not sure, maybe ""
	}

	extraGroups := []string{}
	for verb, group := range verbToGroupMembership {
		var workspaceAttr authorizer.AttributesRecord := a.Clone() // TODO: write out with 17 function calls
		workspaceAttr.Namespace = ""
		workspaceAttr.APIGroup = "tenancy.kcp.dev"
		...
		workspaceAttr.Resource = "workspaces"
		workspaceAttr.Subresource = "content"
		workspaceAttr.Name = cluster.Name
		workspaceAttr.ResourceRequest = true
		workspaceAttr.Verb = verb

		dec, reason, err := scopedRbacAuthorizer.Authorize(ctx, workspaceAttr)
		if dec == Allow {
			extraGroups = append(extraGroups, group)
		}
	}
	if len(extraGroups) == 0 {
		return NoOpinion / Deny, "workspace access not permitted", nil
	}

	aWithExtraGroups := a
	aWithExtraGroups.user.groups = append(aWithExtraGroups.user.groups, withoutEmpty(extraGroups)...)

	return a.delegate.Authorize(ctx, aWithExtraGroups)
}

func (a *OrgWorkspaceAuthorizer) RulesFor(user user.Info, namespace string) ([]ResourceRuleInfo, []NonResourceRuleInfo, bool, error) {
	// nearly as Authorize, but call delegate rules resolver at the end, but also with extraGroups loop
}

type BootstrapPolicyAuthorizer struct {
	roleGetter               rbacv1.RolesGetter
	roleBindingLister        rbaclisterv1.RoleBindingLister
	clusterRoleGetter        rbacv1.ClusterRolesGetter
	clusterRoleBindingLister rbaclisterv1.ClusterRoleBindingLister
}

func (a *BootstrapPolicyAuthorizer) Authorize(ctx context.Context, a authorizer.Attributes) (authorized authorizer.Decision, reason string, err error) {
	scopedRbacAuthorizer := rbac.New(
		a.roleGetter.Scoped(scoped.WithCluster("admin")), // TODO: system:bootstrap in the future
		a.roleBindingLister.Scoped(...),
	...
)

	return scopedRbacAuthorizer.Authorize(ctx, a)
}
*/
