package admin

import (
	"errors"
	"fmt"
	"io"
	"reflect"

	"github.com/spf13/cobra"

	kapi "k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/api/meta"
	"k8s.io/kubernetes/pkg/apimachinery/registered"
	"k8s.io/kubernetes/pkg/kubectl"
	kcmdutil "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/pkg/kubectl/resource"
	"k8s.io/kubernetes/pkg/runtime"

	authorizationapi "github.com/openshift/origin/pkg/authorization/api"
	policyregistry "github.com/openshift/origin/pkg/authorization/registry/policy"
	policyetcd "github.com/openshift/origin/pkg/authorization/registry/policy/etcd"
	policybindingregistry "github.com/openshift/origin/pkg/authorization/registry/policybinding"
	policybindingetcd "github.com/openshift/origin/pkg/authorization/registry/policybinding/etcd"
	rolestorage "github.com/openshift/origin/pkg/authorization/registry/role/policybased"
	rolebindingstorage "github.com/openshift/origin/pkg/authorization/registry/rolebinding/policybased"

	clusterpolicyregistry "github.com/openshift/origin/pkg/authorization/registry/clusterpolicy"
	clusterpolicyetcd "github.com/openshift/origin/pkg/authorization/registry/clusterpolicy/etcd"
	clusterpolicybindingregistry "github.com/openshift/origin/pkg/authorization/registry/clusterpolicybinding"
	clusterpolicybindingetcd "github.com/openshift/origin/pkg/authorization/registry/clusterpolicybinding/etcd"
	clusterrolestorage "github.com/openshift/origin/pkg/authorization/registry/clusterrole/proxy"
	clusterrolebindingstorage "github.com/openshift/origin/pkg/authorization/registry/clusterrolebinding/proxy"
	"github.com/openshift/origin/pkg/authorization/rulevalidation"

	"github.com/openshift/origin/pkg/cmd/cli/describe"
	configapilatest "github.com/openshift/origin/pkg/cmd/server/api/latest"
	cmdclientcmd "github.com/openshift/origin/pkg/cmd/util/clientcmd"
	templateapi "github.com/openshift/origin/pkg/template/api"
	"github.com/openshift/origin/pkg/util/restoptions"
)

const OverwriteBootstrapPolicyCommandName = "overwrite-policy"

type OverwriteBootstrapPolicyOptions struct {
	File             string
	MasterConfigFile string

	Force                        bool
	Out                          io.Writer
	CreateBootstrapPolicyCommand string
}

func NewCommandOverwriteBootstrapPolicy(commandName string, fullName string, createBootstrapPolicyCommand string, out io.Writer) *cobra.Command {
	options := &OverwriteBootstrapPolicyOptions{Out: out}
	options.CreateBootstrapPolicyCommand = createBootstrapPolicyCommand

	cmd := &cobra.Command{
		Use:   commandName,
		Short: "Reset the policy to the default values",
		Run: func(cmd *cobra.Command, args []string) {
			if err := options.Validate(args); err != nil {
				kcmdutil.CheckErr(kcmdutil.UsageError(cmd, err.Error()))
			}

			if err := options.OverwriteBootstrapPolicy(); err != nil {
				kcmdutil.CheckErr(err)
			}
		},
	}

	flags := cmd.Flags()

	flags.BoolVarP(&options.Force, "force", "f", false, "You must confirm you really want to reset your policy. This will delete any custom settings you may have.")
	flags.StringVar(&options.File, "filename", "", "The policy template file containing roles and bindings.  One can be created with '"+createBootstrapPolicyCommand+"'.")
	flags.StringVar(&options.MasterConfigFile, "master-config", "openshift.local.config/master/master-config.yaml", "Location of the master configuration file to run from in order to connect to etcd and directly modify the policy.")

	// autocompletion hints
	cmd.MarkFlagFilename("filename")
	cmd.MarkFlagFilename("master-config", "yaml", "yml")

	return cmd
}

func (o OverwriteBootstrapPolicyOptions) Validate(args []string) error {
	if len(args) != 0 {
		return errors.New("no arguments are supported")
	}
	if len(o.File) == 0 {
		return errors.New("filename must be provided")
	}
	if len(o.MasterConfigFile) == 0 {
		return errors.New("master-config must be provided")
	}

	return nil
}

func (o OverwriteBootstrapPolicyOptions) OverwriteBootstrapPolicy() error {
	masterConfig, err := configapilatest.ReadAndResolveMasterConfig(o.MasterConfigFile)
	if err != nil {
		return err
	}

	optsGetter := restoptions.NewConfigGetter(*masterConfig)

	return OverwriteBootstrapPolicy(optsGetter, o.File, o.CreateBootstrapPolicyCommand, o.Force, o.Out)
}

func OverwriteBootstrapPolicy(optsGetter restoptions.Getter, policyFile, createBootstrapPolicyCommand string, change bool, out io.Writer) error {
	if !change {
		fmt.Fprintf(out, "Performing a dry run of policy overwrite:\n\n")
	}

	mapper := cmdclientcmd.ShortcutExpander{RESTMapper: kubectl.ShortcutExpander{RESTMapper: registered.RESTMapper()}}
	typer := kapi.Scheme
	clientMapper := resource.ClientMapperFunc(func(mapping *meta.RESTMapping) (resource.RESTClient, error) {
		return nil, nil
	})

	r := resource.NewBuilder(mapper, typer, clientMapper, kapi.Codecs.UniversalDecoder()).
		FilenameParam(false, false, policyFile).
		Flatten().
		Do()

	if r.Err() != nil {
		return r.Err()
	}

	policyStorage, err := policyetcd.NewStorage(optsGetter)
	if err != nil {
		return err
	}
	policyRegistry := policyregistry.NewRegistry(policyStorage)

	policyBindingStorage, err := policybindingetcd.NewStorage(optsGetter)
	if err != nil {
		return err
	}
	policyBindingRegistry := policybindingregistry.NewRegistry(policyBindingStorage)

	clusterPolicyStorage, err := clusterpolicyetcd.NewStorage(optsGetter)
	if err != nil {
		return err
	}
	clusterPolicyRegistry := clusterpolicyregistry.NewRegistry(clusterPolicyStorage)

	clusterPolicyBindingStorage, err := clusterpolicybindingetcd.NewStorage(optsGetter)
	if err != nil {
		return err
	}
	clusterPolicyBindingRegistry := clusterpolicybindingregistry.NewRegistry(clusterPolicyBindingStorage)

	ruleResolver := rulevalidation.NewDefaultRuleResolver(
		policyRegistry,
		policyBindingRegistry,
		clusterPolicyRegistry,
		clusterPolicyBindingRegistry,
	)

	roleStorage := rolestorage.NewVirtualStorage(policyRegistry, ruleResolver)
	roleBindingStorage := rolebindingstorage.NewVirtualStorage(policyBindingRegistry, ruleResolver)
	clusterRoleStorage := clusterrolestorage.NewClusterRoleStorage(clusterPolicyRegistry, clusterPolicyBindingRegistry)
	clusterRoleBindingStorage := clusterrolebindingstorage.NewClusterRoleBindingStorage(clusterPolicyRegistry, clusterPolicyBindingRegistry)

	return r.Visit(func(info *resource.Info, err error) error {
		if err != nil {
			return err
		}
		template, ok := info.Object.(*templateapi.Template)
		if !ok {
			return errors.New("policy must be contained in a template.  One can be created with '" + createBootstrapPolicyCommand + "'.")
		}
		runtime.DecodeList(template.Objects, kapi.Codecs.UniversalDecoder())

		for _, item := range template.Objects {
			switch t := item.(type) {
			case *authorizationapi.Role:
				ctx := kapi.WithNamespace(kapi.NewContext(), t.Namespace)
				if change {
					roleStorage.Delete(ctx, t.Name, nil)
					if _, err := roleStorage.CreateRoleWithEscalation(ctx, t); err != nil {
						return err
					}
				} else {
					fmt.Fprintf(out, "Overwrite role %s/%s\n", t.Namespace, t.Name)
					if s, err := describe.DescribeRole(t); err == nil {
						fmt.Fprintf(out, "%s\n", s)
					}
				}
			case *authorizationapi.RoleBinding:
				ctx := kapi.WithNamespace(kapi.NewContext(), t.Namespace)
				if change {
					roleBindingStorage.Delete(ctx, t.Name, nil)
					if _, err := roleBindingStorage.CreateRoleBindingWithEscalation(ctx, t); err != nil {
						return err
					}
				} else {
					fmt.Fprintf(out, "Overwrite role binding %s/%s\n", t.Namespace, t.Name)
					if s, err := describe.DescribeRoleBinding(t, nil, nil); err == nil {
						fmt.Fprintf(out, "%s\n", s)
					}
				}

			case *authorizationapi.ClusterRole:
				ctx := kapi.WithNamespace(kapi.NewContext(), t.Namespace)
				if change {
					clusterRoleStorage.Delete(ctx, t.Name, nil)
					if _, err := clusterRoleStorage.CreateClusterRoleWithEscalation(ctx, t); err != nil {
						return err
					}
				} else {
					fmt.Fprintf(out, "Overwrite role %s/%s\n", t.Namespace, t.Name)
					if s, err := describe.DescribeRole(authorizationapi.ToRole(t)); err == nil {
						fmt.Fprintf(out, "%s\n", s)
					}
				}
			case *authorizationapi.ClusterRoleBinding:
				ctx := kapi.WithNamespace(kapi.NewContext(), t.Namespace)
				if change {
					clusterRoleBindingStorage.Delete(ctx, t.Name, nil)
					if _, err := clusterRoleBindingStorage.CreateClusterRoleBindingWithEscalation(ctx, t); err != nil {
						return err
					}
				} else {
					fmt.Fprintf(out, "Overwrite role binding %s/%s\n", t.Namespace, t.Name)
					if s, err := describe.DescribeRoleBinding(authorizationapi.ToRoleBinding(t), nil, nil); err == nil {
						fmt.Fprintf(out, "%s\n", s)
					}
				}

			default:
				return fmt.Errorf("only roles and rolebindings may be created in this mode, not: %v", reflect.TypeOf(t))
			}
		}
		if !change {
			fmt.Fprintf(out, "To make the changes described above, pass --force\n")
		}
		return nil
	})
}
