package catalog

import (
	"context"
	"fmt"
	"strings"
)

// AssembleHCL is the LOCAL translation entry point: it turns a canonical
// environment (provider + abstract region + components) into the concrete
// per-provider terraform documents, by dispatching each component to the same
// catalog Translate*/Render*HCL pair the round-trip harness uses. This is what
// pyxcloud_environment (Mode A) runs locally with the ambient provider env
// credentials — no backend round-trip, no token, fully testable in Go.
//
// Placement is synthesised: one network (VPC + subnet) and one security group
// per environment, with every VM wired into them. Component config beyond VM
// sizing (rich SG rules, LB listeners, …) will be threaded as the environment
// schema grows; today the env resource feeds VM-centric topologies.
//
// A component type with no catalog translation is a HARD error (never a silent
// drop) — coverage is added component by component, AWS first.

// AssembleVM is the canonical VM sizing for a component (strings mirror the
// resource schema; parsed to ints here).
type AssembleVM struct {
	Architecture    string
	CPU             string
	RAM             string
	OS              string
	OSVersion       string
	UserData        string
	InstanceProfile string
}

// AssembleScaleGroup is the canonical config for a `virtual-machine-scale-group`
// component — VM sizing plus autoscale bounds, health, and the launch-template
// bootstrap (user_data) + instance-profile. Renders to a real ASG (launch template
// + autoscaling group), not a single instance.
type AssembleScaleGroup struct {
	Architecture    string
	CPU             string
	RAM             string
	OS              string
	OSVersion       string
	Min             int
	Max             int
	Desired         int
	Health          string // ec2 | elb
	UserData        string
	InstanceProfile string
	RootDiskGB      int
}

// AssembleAttachToExistingALB is the config for an `attach-to-existing-alb` component.
type AssembleAttachToExistingALB struct {
	ALBListenerARN  string
	HostHeader      string
	Port            int
	Protocol        string
	HealthCheckPath string
	HealthCheckPort string
	ScaleGroup      string
	Priority        int
}

// AssembleNetworkRule is the config for a network-rule component. Target/source
// may point either at a security group produced in this environment by name, or
// at an existing provider security group id.
type AssembleNetworkRule struct {
	Direction             string
	Protocol              string
	FromPort              int
	ToPort                int
	Port                  int
	CIDRs                 []string
	SourceSG              string
	SourceSecurityGroupID string
	TargetSG              string
	TargetSecurityGroupID string
	Description           string
}

// AssembleIAM is the canonical IAM config for an `iam` component.
type AssembleIAM struct {
	AssumeService     string
	InlinePolicies    []IAMPolicy
	ManagedPolicyARNs []string
	InstanceProfile   bool
}

// AssembleComponent is one canonical component in the environment.
type AssembleComponent struct {
	Path                string
	Name                string
	Type                string
	Count               int
	VM                  *AssembleVM
	ScaleGroup          *AssembleScaleGroup
	AttachToExistingALB *AssembleAttachToExistingALB
	NetworkRule         *AssembleNetworkRule
	IAM                 *AssembleIAM
	Monitoring          *AssembleMonitoring
	DNS                 *AssembleDNS
	ObjectStorage       *AssembleObjectStorage
	Secrets             *AssembleSecrets
	MDB                 *AssembleMDB
	Queue               *AssembleQueue
	Stream              *AssembleStream
	Serverless          *AssembleServerless
	KMS                 *AssembleKMS
	Cache               *AssembleCache
	CDN                 *AssembleCDN
	WAF                 *AssembleWAF
	K8s                 *AssembleK8s
	LB                  *AssembleLB
	Email               *AssembleEmail
	BlockStorage        *AssembleBlockStorage
	PrefixList          *AssemblePrefixList
	Synthetics          *AssembleSynthetics
}

// AssembleSynthetics is the config for a `synthetics` / `uptime-check` component.
type AssembleSynthetics struct {
	TargetURL      string
	Runtime        string
	Handler        string
	ScheduleExpr   string
	ArtifactBucket string
	ExecRoleARN    string
}

// AssembleBlockStorage is the config for a `block-storage` component (attaches to a VM).
type AssembleBlockStorage struct {
	SizeGB     int
	VolumeType string
	DeviceName string
	TargetVM   string
}

// AssemblePrefixList is the config for a `prefix-list` component (AWS).
type AssemblePrefixList struct {
	Entries []PrefixEntry
}

// AssembleEmail is the config for an `email` / `email-service` component.
type AssembleEmail struct {
	Domain string
}

// AssembleKMS is the config for a `kms` / `encryption-key` component.
type AssembleKMS struct {
	Description        string
	RotationDays       int
	DeletionWindowDays int
}

// AssembleCache is the config for a `cache` component (network-scoped).
type AssembleCache struct {
	Engine   string
	Version  string
	MemoryGB int
	HA       bool
}

// AssembleCDN is the config for a `cdn-service` / `cdn` component.
type AssembleCDN struct {
	OriginKind string // object-storage | load-balancer
	OriginName string
}

// AssembleWAF is the config for a `waf-service` / `waf` component.
type AssembleWAF struct {
	Scope         string
	AssociateName string
}

// AssembleLBListener is one listener (port + protocol) for a load-balancer.
type AssembleLBListener struct {
	Port     int
	Protocol string
}

// AssembleLB is the config for a `load-balancer` component (network-scoped).
type AssembleLB struct {
	Listeners       []AssembleLBListener
	HealthCheckPath string
	HealthCheckPort int
	HealthProtocol  string
	Stickiness      bool
	TargetKind      string // scale-group | vm (default vm)
	TargetName      string // the VM/scale-group component to front
}

// AssembleK8s is the config for a `managed-kubernetes` / `container-service` component (network-scoped).
type AssembleK8s struct {
	Version      string
	Architecture string
	NodeCPU      int
	NodeRAM      int
	MinNodes     int
	MaxNodes     int
	DesiredNodes int
}

// AssembleMonitoring is the canonical monitoring config for a `monitoring` component.
type AssembleMonitoring struct {
	LogGroups []LogGroup
	Alarms    []MetricAlarm
}

// AssembleDNS is the canonical Cloudflare DNS config for a `dns` component.
type AssembleDNS struct {
	ZoneID  string
	Records []DNSRecord
}

// AssembleObjectStorage is the config for an `object-storage` / `blob-storage` component.
type AssembleObjectStorage struct {
	Versioning bool
	Public     bool
}

// AssembleSecrets is the config for a `secrets-manager` component.
type AssembleSecrets struct {
	Description  string
	RotationDays int
}

// AssembleMDB is the config for a `managed-database` component.
type AssembleMDB struct {
	Engine    string
	Version   string
	CPU       int
	RAM       int
	StorageGB int
	HA        bool
	Encrypted bool
}

// AssembleQueue is the config for a `managed-queue` / `message-queue` component.
type AssembleQueue struct {
	FIFO                     bool
	VisibilityTimeoutSeconds int
	MaxReceiveCount          int
}

// AssembleStream is the config for an `event-streaming` / `event-bus` component.
type AssembleStream struct {
	Shards         int
	RetentionHours int
}

// AssembleServerless is the config for a `serverless-function` component.
type AssembleServerless struct {
	Runtime        string
	RuntimeVersion string
	Handler        string
	MemoryMB       int
	TimeoutSeconds int
	SourceArtifact string
}

// AssembleInput is the catalog-native environment description (no client import,
// so the catalog stays dependency-free).
type AssembleInput struct {
	Name       string
	Provider   string
	Region     string
	CIDR       string   // optional; defaults to 10.0.0.0/16
	Subnets    []string // optional; defaults to a single 10.0.1.0/24
	Expose     []int    // optional security-group TCP expose ports
	Components []AssembleComponent
}

// AssembleHCL translates the environment to concrete terraform documents.
func AssembleHCL(ctx context.Context, cat Catalog, in AssembleInput) ([]string, error) {
	if in.Name == "" {
		return nil, fmt.Errorf("environment: name is required")
	}
	cidr := in.CIDR
	if cidr == "" {
		cidr = "10.0.0.0/16"
	}
	subnets := in.Subnets
	if len(subnets) == 0 {
		// Two subnets by default so a managed-database subnet group is multi-AZ
		// (AWS requires >= 2 AZs); VMs just use the first.
		subnets = []string{"10.0.1.0/24", "10.0.2.0/24"}
	}

	var docs []string
	needsCloudflare := false

	// A network (VPC + subnets) is only needed when the environment places VMs or a
	// managed database. A DNS-only / IAM-only / storage-only env must NOT make a VPC.
	hasVM, hasNetworked := false, false
	for _, c := range in.Components {
		if Mitigatable(c.Type) && !NativelySupported(c.Type, in.Provider) {
			hasNetworked = true // mitigation runs the service on a VM, which needs the network
			continue
		}
		switch c.Type {
		case "virtual-machine", "virtual-machine-scale-group":
			hasVM, hasNetworked = true, true
		case "managed-database", "cache", "managed-kubernetes", "container-service", "load-balancer", "attach-to-existing-alb":
			hasNetworked = true
		}
	}

	netName := in.Name + "-net"
	sgName := in.Name + "-sg"
	subnetName := ""         // first subnet (VM placement)
	var subnetNames []string // all subnets (DB subnet group)
	vmSG := ""

	// 1. Network (VPC + subnets) — when VMs or a managed database are present.
	if hasNetworked {
		netPlan, err := TranslateNetwork(ctx, cat, NetworkSpec{
			Name: netName, Region: in.Region, Provider: in.Provider, CIDR: cidr, Subnets: subnets,
		})
		if err != nil {
			return nil, fmt.Errorf("network: %w", err)
		}
		netHCL, err := RenderHCL(netPlan)
		if err != nil {
			return nil, fmt.Errorf("network render: %w", err)
		}
		docs = append(docs, netHCL)
		if len(netPlan.Subnets) == 0 {
			return nil, fmt.Errorf("network produced no subnets")
		}
		subnetName = netPlan.Subnets[0].Name
		for _, s := range netPlan.Subnets {
			subnetNames = append(subnetNames, s.Name)
		}
	}

	envRuleNeedsSG := false
	for _, c := range in.Components {
		if c.Type == "network-rule" && c.NetworkRule != nil && networkRuleReferencesEnvSG(c.NetworkRule, sgName) {
			envRuleNeedsSG = true
			break
		}
	}

	// 2. Security group — when VMs are present and either ports are exposed or a
	//    network-rule targets/references the environment SG. With only env-rule
	//    references, emit the empty SG directly because TranslateSecurityGroup
	//    intentionally rejects a rule-less SG.
	if hasVM && (len(in.Expose) > 0 || envRuleNeedsSG) {
		sgPlan, err := TranslateSecurityGroup(ctx, cat, SecurityGroupSpec{
			Name: sgName, Network: netName, Region: in.Region, Provider: in.Provider,
			Description: in.Name + " environment", Expose: in.Expose,
		})
		if err != nil && len(in.Expose) > 0 {
			return nil, fmt.Errorf("security-group: %w", err)
		}
		if len(in.Expose) > 0 {
			sgHCL, err := RenderSGHCL(sgPlan)
			if err != nil {
				return nil, fmt.Errorf("security-group render: %w", err)
			}
			docs = append(docs, sgHCL)
		} else {
			sgHCL, err := renderEnvironmentSecurityGroupHCL(in.Provider, sgName, netName, in.Name+" environment")
			if err != nil {
				return nil, fmt.Errorf("security-group render: %w", err)
			}
			docs = append(docs, sgHCL)
		}
		vmSG = sgName
	}

	// 3. Components.
	for _, c := range in.Components {
		// Mitigation: provider lacks the managed service -> self-host it on a VM.
		if Mitigatable(c.Type) && !NativelySupported(c.Type, in.Provider) {
			mdocs, err := mitigateComponent(ctx, cat, in.Provider, in.Region, c, netName, subnetName, vmSG)
			if err != nil {
				return nil, err
			}
			docs = append(docs, mdocs...)
			continue
		}
		switch c.Type {
		case "virtual-machine":
			if c.VM == nil {
				return nil, fmt.Errorf("component %q (%s): vm sizing is required", c.Name, c.Type)
			}
			vmPlan, err := TranslateVM(ctx, cat, VMSpec{
				Name: c.Name, Region: in.Region, Provider: in.Provider,
				Architecture: c.VM.Architecture, CPU: atoiOrZero(c.VM.CPU), RAM: atoiOrZero(c.VM.RAM),
				OS: c.VM.OS, OSVersion: c.VM.OSVersion, Count: c.Count,
				Network: netName, Subnet: subnetName, SecurityGroup: vmSG,
				UserData: c.VM.UserData, InstanceProfile: c.VM.InstanceProfile,
			})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			vmHCL, err := RenderVMHCL(vmPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, vmHCL)
		case "virtual-machine-scale-group":
			if c.ScaleGroup == nil {
				return nil, fmt.Errorf("component %q (%s): scale_group config is required", c.Name, c.Type)
			}
			sg := c.ScaleGroup
			sgPlan, err := TranslateScaleGroup(ctx, cat, ScaleGroupSpec{
				Name: c.Name, Region: in.Region, Provider: in.Provider,
				Architecture: sg.Architecture, CPU: atoiOrZero(sg.CPU), RAM: atoiOrZero(sg.RAM),
				OS: sg.OS, OSVersion: sg.OSVersion,
				Min: sg.Min, Max: sg.Max, Desired: sg.Desired, Health: sg.Health,
				UserData: sg.UserData, InstanceProfile: sg.InstanceProfile, RootDiskGB: sg.RootDiskGB,
				Network: netName, SecurityGroup: vmSG, Subnets: subnetNames,
			})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			sgHCL, err := RenderScaleGroupHCL(sgPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, sgHCL)
		case "attach-to-existing-alb":
			if c.AttachToExistingALB == nil {
				return nil, fmt.Errorf("component %q (%s): attach_to_existing_alb config is required", c.Name, c.Type)
			}
			att := c.AttachToExistingALB
			attPlan, err := TranslateAttachToExistingALB(ctx, cat, AttachToExistingALBSpec{
				Name:            c.Name,
				Region:          in.Region,
				Provider:        in.Provider,
				ALBListenerARN:  att.ALBListenerARN,
				HostHeader:      att.HostHeader,
				Port:            att.Port,
				Protocol:        att.Protocol,
				HealthCheckPath: att.HealthCheckPath,
				HealthCheckPort: att.HealthCheckPort,
				ScaleGroup:      att.ScaleGroup,
				Priority:        att.Priority,
				Network:         netName,
			})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			attHCL, err := RenderAttachToExistingALBHCL(attPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, attHCL)
		case "network-rule":
			if c.NetworkRule == nil {
				return nil, fmt.Errorf("component %q (network-rule): config is required", c.Name)
			}
			ruleHCL, err := renderNetworkRuleHCL(in.Provider, c.Name, c.NetworkRule)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, ruleHCL)
		case "iam":
			iamSpec := IAMSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.IAM != nil {
				iamSpec.AssumeService = c.IAM.AssumeService
				iamSpec.InlinePolicies = c.IAM.InlinePolicies
				iamSpec.ManagedPolicyARNs = c.IAM.ManagedPolicyARNs
				iamSpec.InstanceProfile = c.IAM.InstanceProfile
			}
			iamPlan, err := TranslateIAM(ctx, cat, iamSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			iamHCL, err := RenderIAMHCL(iamPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, iamHCL)
		case "monitoring":
			if c.Monitoring == nil {
				return nil, fmt.Errorf("component %q (monitoring): config is required", c.Name)
			}
			monPlan, err := TranslateMonitoring(ctx, cat, MonitoringSpec{
				Name: c.Name, Region: in.Region, Provider: in.Provider,
				LogGroups: c.Monitoring.LogGroups, Alarms: c.Monitoring.Alarms,
			})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			monHCL, err := RenderMonitoringHCL(monPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, monHCL)
		case "dns":
			if c.DNS == nil {
				return nil, fmt.Errorf("component %q (dns): config is required", c.Name)
			}
			dnsPlan, err := TranslateCloudflareDNS(ctx, CloudflareDNSSpec{
				Name: c.Name, ZoneID: c.DNS.ZoneID, Records: c.DNS.Records,
			})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			dnsHCL, err := RenderCloudflareDNSHCL(dnsPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			needsCloudflare = true
			docs = append(docs, dnsHCL)
		case "object-storage", "blob-storage":
			osSpec := ObjectStorageSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.ObjectStorage != nil {
				osSpec.Versioning = c.ObjectStorage.Versioning
				osSpec.Public = c.ObjectStorage.Public
			}
			osPlan, err := TranslateObjectStorage(ctx, cat, osSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			osHCL, err := RenderObjectStorageHCL(osPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, osHCL)
		case "secrets-manager":
			secSpec := SecretsSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.Secrets != nil {
				secSpec.Description = c.Secrets.Description
				secSpec.RotationDays = c.Secrets.RotationDays
			}
			secPlan, err := TranslateSecrets(ctx, cat, secSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			secHCL, err := RenderSecretsHCL(secPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, secHCL)
		case "managed-database":
			if c.MDB == nil {
				return nil, fmt.Errorf("component %q (managed-database): config is required", c.Name)
			}
			mdbPlan, err := TranslateManagedDatabase(ctx, cat, ManagedDatabaseSpec{
				Name: c.Name, Region: in.Region, Provider: in.Provider,
				Engine: c.MDB.Engine, Version: c.MDB.Version, CPU: c.MDB.CPU, RAM: c.MDB.RAM,
				StorageGB: c.MDB.StorageGB, HA: c.MDB.HA, Encrypted: c.MDB.Encrypted,
				Network: netName, Subnets: subnetNames, SecurityGroup: vmSG,
			})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			mdbHCL, err := RenderManagedDatabaseHCL(mdbPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, mdbHCL)
		case "managed-queue", "message-queue":
			qSpec := QueueSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.Queue != nil {
				qSpec.FIFO = c.Queue.FIFO
				qSpec.VisibilityTimeoutSeconds = c.Queue.VisibilityTimeoutSeconds
				qSpec.MaxReceiveCount = c.Queue.MaxReceiveCount
			}
			qPlan, err := TranslateQueue(ctx, cat, qSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			qHCL, err := RenderMessagingHCL(qPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, qHCL)
		case "event-streaming", "event-bus":
			sSpec := StreamSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.Stream != nil {
				sSpec.Shards = c.Stream.Shards
				sSpec.RetentionHours = c.Stream.RetentionHours
			}
			sPlan, err := TranslateStream(ctx, cat, sSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			sHCL, err := RenderMessagingHCL(sPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, sHCL)
		case "serverless-function":
			slSpec := ServerlessSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.Serverless != nil {
				slSpec.Runtime = c.Serverless.Runtime
				slSpec.RuntimeVersion = c.Serverless.RuntimeVersion
				slSpec.Handler = c.Serverless.Handler
				slSpec.MemoryMB = c.Serverless.MemoryMB
				slSpec.TimeoutSeconds = c.Serverless.TimeoutSeconds
				slSpec.SourceArtifact = c.Serverless.SourceArtifact
			}
			slPlan, err := TranslateServerless(ctx, cat, slSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			slHCL, err := RenderServerlessHCL(slPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, slHCL)
		case "kms", "encryption-key":
			kmsSpec := KMSSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.KMS != nil {
				kmsSpec.Description = c.KMS.Description
				kmsSpec.RotationDays = c.KMS.RotationDays
				kmsSpec.DeletionWindowDays = c.KMS.DeletionWindowDays
			}
			kmsPlan, err := TranslateKMS(ctx, cat, kmsSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			kmsHCL, err := RenderKMSHCL(kmsPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, kmsHCL)
		case "cache":
			cSpec := CacheSpec{Name: c.Name, Region: in.Region, Provider: in.Provider,
				Network: netName, Subnets: subnetNames, SecurityGroup: vmSG}
			if c.Cache != nil {
				cSpec.Engine = c.Cache.Engine
				cSpec.Version = c.Cache.Version
				cSpec.MemoryGB = c.Cache.MemoryGB
				cSpec.HA = c.Cache.HA
			}
			cPlan, err := TranslateCache(ctx, cat, cSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			cHCL, err := RenderCacheHCL(cPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, cHCL)
		case "cdn-service", "cdn":
			if c.CDN == nil {
				return nil, fmt.Errorf("component %q (cdn): config is required", c.Name)
			}
			cdnPlan, err := TranslateCDN(ctx, cat, CDNSpec{
				Name: c.Name, Region: in.Region, Provider: in.Provider,
				OriginKind: c.CDN.OriginKind, OriginName: c.CDN.OriginName,
			})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			cdnHCL, err := RenderCDNHCL(cdnPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, cdnHCL)
		case "waf-service", "waf":
			wafSpec := WAFSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.WAF != nil {
				wafSpec.Scope = c.WAF.Scope
				wafSpec.AssociateName = c.WAF.AssociateName
			}
			wafPlan, err := TranslateWAF(ctx, cat, wafSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			wafHCL, err := RenderWAFHCL(wafPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, wafHCL)
		case "managed-kubernetes", "container-service":
			kSpec := K8sSpec{Name: c.Name, Region: in.Region, Provider: in.Provider,
				Network: netName, Subnets: subnetNames}
			if c.K8s != nil {
				kSpec.Version = c.K8s.Version
				kSpec.Architecture = c.K8s.Architecture
				kSpec.NodeCPU = c.K8s.NodeCPU
				kSpec.NodeRAM = c.K8s.NodeRAM
				kSpec.MinNodes = c.K8s.MinNodes
				kSpec.MaxNodes = c.K8s.MaxNodes
				kSpec.DesiredNodes = c.K8s.DesiredNodes
			}
			kPlan, err := TranslateKubernetes(ctx, cat, kSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			kHCL, err := RenderKubernetesHCL(kPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, kHCL)
		case "load-balancer":
			lbSpec := LoadBalancerSpec{
				Name: c.Name, Region: in.Region, Provider: in.Provider,
				Network: netName, Subnets: subnetNames, SecurityGroup: vmSG,
				TargetKind: LBTargetVM,
			}
			if c.LB != nil {
				for _, l := range c.LB.Listeners {
					lbSpec.Listeners = append(lbSpec.Listeners, LBListenerSpec{Port: l.Port, Protocol: l.Protocol})
				}
				lbSpec.HealthCheck = LBHealthCheckSpec{Protocol: c.LB.HealthProtocol, Port: c.LB.HealthCheckPort, Path: c.LB.HealthCheckPath}
				lbSpec.Stickiness = c.LB.Stickiness
				if c.LB.TargetKind != "" {
					lbSpec.TargetKind = c.LB.TargetKind
				}
				lbSpec.TargetName = c.LB.TargetName
			}
			if len(lbSpec.Listeners) == 0 {
				lbSpec.Listeners = []LBListenerSpec{{Port: 80, Protocol: "http"}}
			}
			lbPlan, err := TranslateLoadBalancer(ctx, cat, lbSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			lbHCL, err := RenderLoadBalancerHCL(lbPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, lbHCL)
		case "email", "email-service":
			if c.Email == nil {
				return nil, fmt.Errorf("component %q (email): config is required", c.Name)
			}
			emPlan, err := TranslateEmail(ctx, cat, EmailSpec{Name: c.Name, Region: in.Region, Provider: in.Provider, Domain: c.Email.Domain})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			emHCL, err := RenderEmailHCL(emPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, emHCL)
		case "block-storage":
			if c.BlockStorage == nil {
				return nil, fmt.Errorf("component %q (block-storage): config is required", c.Name)
			}
			bsPlan, err := TranslateBlockStorage(ctx, cat, BlockStorageSpec{
				Name: c.Name, Region: in.Region, Provider: in.Provider,
				SizeGB: c.BlockStorage.SizeGB, VolumeType: c.BlockStorage.VolumeType,
				DeviceName: c.BlockStorage.DeviceName, TargetVM: c.BlockStorage.TargetVM,
			})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			bsHCL, err := RenderBlockStorageHCL(bsPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, bsHCL)
		case "prefix-list":
			if c.PrefixList == nil {
				return nil, fmt.Errorf("component %q (prefix-list): config is required", c.Name)
			}
			plPlan, err := TranslatePrefixList(ctx, cat, PrefixListSpec{
				Name: c.Name, Region: in.Region, Provider: in.Provider, Entries: c.PrefixList.Entries,
			})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			plHCL, err := RenderPrefixListHCL(plPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, plHCL)
		case "synthetics", "uptime-check":
			synSpec := SyntheticsSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.Synthetics != nil {
				synSpec.TargetURL = c.Synthetics.TargetURL
				synSpec.Runtime = c.Synthetics.Runtime
				synSpec.Handler = c.Synthetics.Handler
				synSpec.ScheduleExpr = c.Synthetics.ScheduleExpr
				synSpec.ArtifactBucket = c.Synthetics.ArtifactBucket
				synSpec.ExecRoleARN = c.Synthetics.ExecRoleARN
			}
			synPlan, err := TranslateSynthetics(ctx, cat, synSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			synHCL, err := RenderSyntheticsHCL(synPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, synHCL)
		default:
			return nil, fmt.Errorf("component %q: type %q is not yet supported by local assembly "+
				"(coverage is added component by component, AWS first)", c.Name, c.Type)
		}
	}
	// Declare the out-of-band db_password variable once when any managed-database is
	// present — the managed_database render references var.db_password (the password
	// is supplied out of band, never in the topology/state).
	for _, c := range in.Components {
		if c.Type == "managed-database" {
			docs = append([]string{"variable \"db_password\" {\n  type      = string\n  sensitive = true\n}\n"}, docs...)
			break
		}
	}
	// Emit a required_providers block when one is needed: a non-default-namespace
	// cloud provider (e.g. digitalocean/digitalocean) always needs its source pinned,
	// and once ANY required_providers entry exists (e.g. Cloudflare) terraform also
	// requires the cloud provider declared. AWS-only envs need NO block (hashicorp/aws
	// auto-installs), keeping the common case clean.
	if rp := requiredProvidersBlock(in.Provider, needsCloudflare); rp != "" {
		docs = append([]string{rp}, docs...)
	}
	return docs, nil
}

func networkRuleReferencesEnvSG(r *AssembleNetworkRule, envSG string) bool {
	return r.TargetSG == envSG || r.SourceSG == envSG
}

func renderEnvironmentSecurityGroupHCL(provider, name, network, description string) (string, error) {
	if strings.ToLower(provider) != ProviderAWS {
		return "", fmt.Errorf("network-rule-created environment security groups are only supported on AWS for now")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"aws_security_group\" %q {\n", tfName(name))
	fmt.Fprintf(&b, "  name        = %q\n", name)
	fmt.Fprintf(&b, "  description = %q\n", asciiOnly(description))
	fmt.Fprintf(&b, "  vpc_id      = data.aws_vpc.default.id\n")
	b.WriteString("\n")
	b.WriteString("  egress {\n")
	b.WriteString("    from_port   = 0\n")
	b.WriteString("    to_port     = 0\n")
	b.WriteString("    protocol    = \"-1\"\n")
	b.WriteString("    cidr_blocks = [\"0.0.0.0/0\"]\n")
	b.WriteString("  }\n")
	b.WriteString("\n")
	fmt.Fprintf(&b, "  tags = { Name = %q, pyxcloud = \"true\" }\n", name)
	b.WriteString("}\n")
	_ = network
	return b.String(), nil
}

func renderNetworkRuleHCL(provider, name string, rule *AssembleNetworkRule) (string, error) {
	if strings.ToLower(provider) != ProviderAWS {
		return "", fmt.Errorf("network-rule is only supported on AWS for now")
	}
	direction := strings.ToLower(strings.TrimSpace(rule.Direction))
	if direction == "" {
		direction = DirIngress
	}
	protocol := strings.ToLower(strings.TrimSpace(rule.Protocol))
	if protocol == "" {
		protocol = ProtoTCP
	}
	fromPort, toPort := rule.FromPort, rule.ToPort
	if rule.Port > 0 {
		fromPort, toPort = rule.Port, rule.Port
	}
	if toPort == 0 {
		toPort = fromPort
	}
	if fromPort <= 0 || toPort <= 0 {
		return "", fmt.Errorf("network-rule requires port or from_port/to_port")
	}
	target := securityGroupRef(rule.TargetSG, rule.TargetSecurityGroupID)
	if target == "" {
		return "", fmt.Errorf("network-rule requires target_sg or target_security_group_id")
	}
	source := securityGroupRef(rule.SourceSG, rule.SourceSecurityGroupID)
	cidrs := rule.CIDRs
	if source == "" && len(cidrs) == 0 {
		return "", fmt.Errorf("network-rule requires source_sg, source_security_group_id, or cidrs")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "resource \"aws_security_group_rule\" %q {\n", tfName(name))
	fmt.Fprintf(&b, "  type              = %q\n", direction)
	fmt.Fprintf(&b, "  security_group_id = %s\n", target)
	fmt.Fprintf(&b, "  protocol          = %q\n", awsProto(protocol))
	fmt.Fprintf(&b, "  from_port         = %d\n", fromPort)
	fmt.Fprintf(&b, "  to_port           = %d\n", toPort)
	if source != "" {
		fmt.Fprintf(&b, "  source_security_group_id = %s\n", source)
	} else {
		v4, v6 := splitCIDRs(cidrs)
		if len(v4) > 0 {
			fmt.Fprintf(&b, "  cidr_blocks       = %s\n", hclCIDRList(v4))
		}
		if len(v6) > 0 {
			fmt.Fprintf(&b, "  ipv6_cidr_blocks  = %s\n", hclCIDRList(v6))
		}
	}
	if rule.Description != "" {
		fmt.Fprintf(&b, "  description       = %q\n", asciiOnly(rule.Description))
	}
	b.WriteString("}\n")
	return b.String(), nil
}

func securityGroupRef(name, id string) string {
	if id != "" {
		return fmt.Sprintf("%q", id)
	}
	if name == "" {
		return ""
	}
	return fmt.Sprintf("aws_security_group.%s.id", tfName(name))
}

// cloudProviderSource maps a provider to its (local-name, registry-source) for a
// terraform required_providers block. The local name matches the resource prefix
// (aws_*, google_*, digitalocean_*, …).
var cloudProviderSource = map[string][2]string{
	ProviderAWS:          {"aws", "hashicorp/aws"},
	ProviderGCP:          {"google", "hashicorp/google"},
	ProviderDigitalOcean: {"digitalocean", "digitalocean/digitalocean"},
	ProviderAzure:        {"azurerm", "hashicorp/azurerm"},
	ProviderLinode:       {"linode", "linode/linode"},
	ProviderUbicloud:     {"ubicloud", "ubicloud/ubicloud"},
	ProviderOracle:       {"oci", "oracle/oci"},
	ProviderIBM:          {"ibm", "IBM-Cloud/ibm"},
	ProviderAlibaba:      {"alicloud", "aliyun/alicloud"},
	ProviderOVH:          {"ovh", "ovh/ovh"},
	ProviderStackIt:      {"stackit", "stackitcloud/stackit"},
}

// requiredProvidersBlock returns the terraform{required_providers{...}} HCL when
// one is needed (non-default cloud source, or Cloudflare present), else "".
func requiredProvidersBlock(provider string, needsCloudflare bool) string {
	src, ok := cloudProviderSource[strings.ToLower(provider)]
	cloudNonDefault := ok && !strings.HasPrefix(src[1], "hashicorp/")
	if !needsCloudflare && !cloudNonDefault {
		return "" // AWS/GCP/Azure-only: hashicorp namespace auto-installs, no block needed
	}
	var b strings.Builder
	b.WriteString("terraform {\n  required_providers {\n")
	if ok {
		fmt.Fprintf(&b, "    %s = {\n      source = %q\n    }\n", src[0], src[1])
	}
	if needsCloudflare {
		b.WriteString("    cloudflare = {\n      source = \"cloudflare/cloudflare\"\n    }\n")
	}
	b.WriteString("  }\n}\n")
	return b.String()
}
