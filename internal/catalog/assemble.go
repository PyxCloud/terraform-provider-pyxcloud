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
	// KubernetesVersion pins the DOKS control-plane version when the scale-group
	// is placed on DigitalOcean (mapped to a digitalocean_kubernetes_cluster
	// node_pool). Empty -> "latest". Other providers ignore it.
	KubernetesVersion string
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
	ScheduledTrigger    *AssembleScheduledTrigger
	KeyValueStore       *AssembleKeyValueStore
	ContainerRegistry   *AssembleContainerRegistry
	ReservedIP          *AssembleReservedIP
	TLSCertificate      *AssembleTLSCertificate
	Tracing             *AssembleTracing
}

// AssembleTracing is the config for a `tracing` component (X-Ray -> Grafana Tempo
// + an OpenTelemetry collector on DOKS).
type AssembleTracing struct {
	SamplingRate   float64
	ClusterName    string
	Namespace      string
	TempoImage     string
	CollectorImage string
	RetentionHours int
}

// AssembleTLSCertificate is the config for a `tls-certificate` / `cert-manager`
// component (ACM -> cert-manager + Let's Encrypt on DOKS).
type AssembleTLSCertificate struct {
	Domains      []string
	Email        string
	Production   bool
	ClusterName  string
	Namespace    string
	DNSChallenge bool
}

// AssembleScheduledTrigger is the config for a `scheduled-trigger` component
// (EventBridge cron -> DOKS CronJob).
type AssembleScheduledTrigger struct {
	Schedule     string
	Image        string
	Command      []string
	ClusterName  string
	Namespace    string
	InvokeTarget string
}

// AssembleKeyValueStore is the config for a `key-value-store` component
// (DynamoDB -> DO Managed Redis).
type AssembleKeyValueStore struct {
	PartitionKey string
	MemoryGB     int
	HA           bool
}

// AssembleContainerRegistry is the config for a `container-registry` /
// `image-registry` component (region-scoped; DO is the AWS-ECR migration target).
type AssembleContainerRegistry struct {
	Tier              string // DO subscription tier: starter | basic | professional
	GarbageCollection bool   // DO server-side garbage collection (opt-in)
	ImmutableTags     bool   // AWS ECR IMMUTABLE image tags (opt-in)
}

// AssembleReservedIP is the config for a `reserved-ip` / `static-ip` /
// `elastic-ip` component (region-scoped; DO is the AWS-EIP migration target for
// the VPN stable endpoint).
type AssembleReservedIP struct {
	AttachTo string // canonical compute target name to bind the IP to ("" = unattached)
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
	// Rules are optional layer-7 routing rules (pd-MIG-LB-L7-ROUTING) — ALB
	// listener-rule parity: per-rule host/path match, priority, and the admin-VPN
	// source-IP gate. Empty = a single default forward action.
	Rules []AssembleLBRoutingRule
}

// AssembleLBRoutingRule is one layer-7 routing rule on a listener.
type AssembleLBRoutingRule struct {
	Priority      int
	HostHeaders   []string
	PathPatterns  []string
	AdminVPNCIDRs []string // admin/VPN source-IP gate
	TargetName    string
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

	// pd-MIG-OBJSTORE-PARITY: S3->Spaces feature parity carried through the assembler.
	Lifecycle    []LifecycleRule  // object-lifecycle rules
	SSE          *SSEConfig       // server-side encryption at rest
	BucketPolicy string           // bucket-policy JSON
	AccessLogs   *AccessLogConfig // server access logging
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
	Name     string
	Provider string
	Region   string
	CIDR     string   // optional; defaults to 10.0.0.0/16
	Subnets  []string // optional; defaults to a single 10.0.1.0/24
	Expose   []int    // optional security-group TCP expose ports
	// IngressRules are explicit ingress rules layered on top of Expose — used to
	// scope a port to an external SG (e.g. a shared ALB SG) instead of 0.0.0.0/0.
	IngressRules []SecurityRule
	Components   []AssembleComponent

	// ApplySecurityBaseline opts the environment into the deploy-default security
	// baseline (pd-DEP-SECURITY-BASELINE): least-privilege egress on the environment
	// security-group (DNS/HTTPS/NTP only, replacing the allow-all default) and
	// production-safe secrets defaults (keep the recovery window). Derived from the
	// topology by DeriveSecurityBaseline; additive and never widens access. Off by
	// default so existing callers are unchanged; the deploy path turns it on.
	ApplySecurityBaseline bool
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

	// Derive the deploy-default security baseline from the topology (pd-DEP-SECURITY-
	// BASELINE) when opted in. It supplies least-privilege egress for the environment
	// SG and production-safe secrets defaults below; empty fields mean "no baseline".
	var baseline SecurityBaseline
	if in.ApplySecurityBaseline {
		baseline = DeriveSecurityBaseline(in)
	}

	managedInstanceProfiles := make(map[string]bool)
	for _, c := range in.Components {
		if c.Type == "access-policy" {
			managedInstanceProfiles[c.Name] = true
		} else if c.Type == "iam" && c.IAM != nil && c.IAM.InstanceProfile {
			managedInstanceProfiles[c.Name] = true
		}
	}

	var docs []string
	needsCloudflare := false
	// needsKubernetes pins the hashicorp/kubernetes provider when a component emits
	// in-cluster resources (DOKS CronJob, cert-manager manifests) — otherwise
	// `terraform plan` cannot resolve kubernetes_* resources.
	needsKubernetes := false
	// needsHelm pins the hashicorp/helm provider when a component installs an
	// upstream operator via helm_release (the operator-pattern CORE — tracing's
	// OTel/Tempo operators, cert-manager's chart). Mirrors needsKubernetes.
	needsHelm := false

	// A network (VPC + subnets) is only needed when the environment places VMs or a
	// managed database. A DNS-only / IAM-only / storage-only env must NOT make a VPC.
	hasVM, hasNetworked := false, false
	for _, c := range in.Components {
		if Mitigatable(c.Type) && !NativelySupported(c.Type, in.Provider) {
			// Mitigation runs the service on a VM, which needs network placement and
			// should receive the environment security group when expose rules exist.
			hasVM, hasNetworked = true, true
			continue
		}
		switch c.Type {
		case "virtual-machine", "virtual-machine-scale-group":
			hasVM, hasNetworked = true, true
		case "managed-database", "cache", "managed-kubernetes", "container-service", "load-balancer", "attach-to-existing-alb",
			"key-value-store", "kv-store", "keyvalue-store", "dynamodb":
			// key-value-store on DO is a private Managed Redis cluster wired to the
			// place's VPC; on AWS DynamoDB is networkless but a VPC does no harm.
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

	// 2. Security group — only when VMs are present AND ports are exposed. A SG with
	//    no rule is rejected by the translator, so with no expose we skip it and the
	//    VMs fall back to the VPC default SG. vmSG is the name to wire onto VMs ("" = none).
	if hasVM && (len(in.Expose) > 0 || len(in.IngressRules) > 0) {
		p := strings.ToLower(in.Provider)
		var rules []SecurityRule
		if p == ProviderDigitalOcean || p == ProviderLinode || p == ProviderStackIt {
			rules = []SecurityRule{
				{
					Direction: DirEgress,
					Protocol:  ProtoTCP,
					FromPort:  1,
					ToPort:    65535,
					CIDRs:     []string{"0.0.0.0/0", "::/0"},
				},
				{
					Direction: DirEgress,
					Protocol:  ProtoUDP,
					FromPort:  1,
					ToPort:    65535,
					CIDRs:     []string{"0.0.0.0/0", "::/0"},
				},
				{
					Direction: DirEgress,
					Protocol:  ProtoICMP,
					FromPort:  0,
					ToPort:    0,
					CIDRs:     []string{"0.0.0.0/0", "::/0"},
				},
			}
		} else {
			rules = []SecurityRule{
				{
					Direction: DirEgress,
					Protocol:  ProtoAll,
					FromPort:  0,
					ToPort:    0,
					CIDRs:     []string{"0.0.0.0/0", "::/0"},
				},
			}
		}
		// Security baseline (pd-DEP-SECURITY-BASELINE): when opted in, REPLACE the
		// allow-all egress default above with the derived least-privilege egress
		// (DNS/HTTPS/NTP). Authors needing wider egress add explicit IngressRules/rules.
		if len(baseline.EgressRules) > 0 {
			rules = append([]SecurityRule(nil), baseline.EgressRules...)
		}
		// Layer explicit ingress rules (e.g. ALB-scoped service doors) on top of expose.
		rules = append(rules, in.IngressRules...)
		sgPlan, err := TranslateSecurityGroup(ctx, cat, SecurityGroupSpec{
			Name: sgName, Network: netName, Region: in.Region, Provider: in.Provider,
			Description: in.Name + " environment", Expose: in.Expose, Rules: rules,
		})
		if err != nil {
			return nil, fmt.Errorf("security-group: %w", err)
		}
		sgHCL, err := RenderSGHCL(sgPlan)
		if err != nil {
			return nil, fmt.Errorf("security-group render: %w", err)
		}
		docs = append(docs, sgHCL)
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
			vmPlan.InstanceProfileManaged = managedInstanceProfiles[c.VM.InstanceProfile]
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
				KubernetesVersion: sg.KubernetesVersion,
				Network:           netName, SecurityGroup: vmSG, Subnets: subnetNames,
			})
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			sgPlan.InstanceProfileManaged = managedInstanceProfiles[sg.InstanceProfile]
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
		case "access-policy":
			if c.IAM != nil && (c.IAM.AssumeService != "" || c.IAM.InstanceProfile) {
				iamSpec := IAMSpec{
					Name:              c.Name,
					Region:            in.Region,
					Provider:          in.Provider,
					AssumeService:     c.IAM.AssumeService,
					InlinePolicies:    c.IAM.InlinePolicies,
					ManagedPolicyARNs: c.IAM.ManagedPolicyARNs,
					InstanceProfile:   c.IAM.InstanceProfile,
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
				break
			}
			iamSpec := IAMSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.IAM != nil {
				iamSpec.InlinePolicies = c.IAM.InlinePolicies
				iamSpec.ManagedPolicyARNs = c.IAM.ManagedPolicyARNs
			}
			apPlan, err := TranslateAccessPolicy(ctx, cat, iamSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			apHCL, err := RenderAccessPolicyHCL(apPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, apHCL)
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
				osSpec.Lifecycle = c.ObjectStorage.Lifecycle
				osSpec.SSE = c.ObjectStorage.SSE
				osSpec.BucketPolicy = c.ObjectStorage.BucketPolicy
				osSpec.AccessLogs = c.ObjectStorage.AccessLogs
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
		case "container-registry", "image-registry":
			crSpec := ContainerRegistrySpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.ContainerRegistry != nil {
				crSpec.Tier = c.ContainerRegistry.Tier
				crSpec.GarbageCollection = c.ContainerRegistry.GarbageCollection
				crSpec.ImmutableTags = c.ContainerRegistry.ImmutableTags
			}
			crPlan, err := TranslateContainerRegistry(ctx, cat, crSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			crHCL, err := RenderContainerRegistryHCL(crPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, crHCL)
		case "reserved-ip", "static-ip", "elastic-ip":
			ripSpec := ReservedIPSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.ReservedIP != nil {
				ripSpec.AttachTo = c.ReservedIP.AttachTo
			}
			ripPlan, err := TranslateReservedIP(ctx, cat, ripSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			ripHCL, err := RenderReservedIPHCL(ripPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, ripHCL)
		case "secrets-manager":
			secSpec := SecretsSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.Secrets != nil {
				secSpec.Description = c.Secrets.Description
				secSpec.RotationDays = c.Secrets.RotationDays
			}
			// Security baseline: keep the provider recovery window (force_destroy=false)
			// so an accidental delete is recoverable (pd-DEP-SECURITY-BASELINE).
			if baseline.SecretsForceDestroy != nil {
				secSpec.ForceDestroy = baseline.SecretsForceDestroy
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
					ls := LBListenerSpec{Port: l.Port, Protocol: l.Protocol}
					for _, r := range l.Rules {
						ls.Rules = append(ls.Rules, LBRoutingRule{
							Priority:      r.Priority,
							HostHeaders:   r.HostHeaders,
							PathPatterns:  r.PathPatterns,
							AdminVPNCIDRs: r.AdminVPNCIDRs,
							TargetName:    r.TargetName,
						})
					}
					lbSpec.Listeners = append(lbSpec.Listeners, ls)
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
			// A DigitalOcean LB with layer-7 routing rules emits a DOKS Ingress
			// (kubernetes_manifest) — pin hashicorp/kubernetes so terraform can plan it.
			if lbPlan.Provider == ProviderDigitalOcean && hasLBRoutingRules(lbPlan.Listeners) {
				needsKubernetes = true
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
		case "scheduled-trigger", "cron-job", "scheduled-task":
			stSpec := ScheduledTriggerSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.ScheduledTrigger != nil {
				stSpec.Schedule = c.ScheduledTrigger.Schedule
				stSpec.Image = c.ScheduledTrigger.Image
				stSpec.Command = c.ScheduledTrigger.Command
				stSpec.ClusterName = c.ScheduledTrigger.ClusterName
				stSpec.Namespace = c.ScheduledTrigger.Namespace
				stSpec.InvokeTarget = c.ScheduledTrigger.InvokeTarget
			}
			stPlan, err := TranslateScheduledTrigger(ctx, cat, stSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			stHCL, err := RenderScheduledTriggerHCL(stPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			if stPlan.ResourceType == "kubernetes_cron_job_v1" {
				needsKubernetes = true
			}
			docs = append(docs, stHCL)
		case "key-value-store", "kv-store", "keyvalue-store", "dynamodb":
			kvSpec := KeyValueStoreSpec{Name: c.Name, Region: in.Region, Provider: in.Provider, Network: netName}
			if c.KeyValueStore != nil {
				kvSpec.PartitionKey = c.KeyValueStore.PartitionKey
				kvSpec.MemoryGB = c.KeyValueStore.MemoryGB
				kvSpec.HA = c.KeyValueStore.HA
			}
			kvPlan, err := TranslateKeyValueStore(ctx, cat, kvSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			kvHCL, err := RenderKeyValueStoreHCL(kvPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			docs = append(docs, kvHCL)
		case "tls-certificate", "certificate", "cert-manager", "managed-certificate":
			tcSpec := TLSCertificateSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.TLSCertificate != nil {
				tcSpec.Domains = c.TLSCertificate.Domains
				tcSpec.Email = c.TLSCertificate.Email
				tcSpec.Production = c.TLSCertificate.Production
				tcSpec.ClusterName = c.TLSCertificate.ClusterName
				tcSpec.Namespace = c.TLSCertificate.Namespace
				tcSpec.DNSChallenge = c.TLSCertificate.DNSChallenge
			}
			tcPlan, err := TranslateTLSCertificate(ctx, cat, tcSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			tcHCL, err := RenderTLSCertificateHCL(tcPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			if tcPlan.ResourceType == "kubernetes_manifest" {
				needsKubernetes = true
			}
			if tcPlan.RendersHelm {
				needsHelm = true
			}
			docs = append(docs, tcHCL)
		case "tracing", "distributed-tracing", "tempo", "trace-collector", "otel-tracing":
			trSpec := TracingSpec{Name: c.Name, Region: in.Region, Provider: in.Provider}
			if c.Tracing != nil {
				trSpec.SamplingRate = c.Tracing.SamplingRate
				trSpec.ClusterName = c.Tracing.ClusterName
				trSpec.Namespace = c.Tracing.Namespace
				trSpec.TempoImage = c.Tracing.TempoImage
				trSpec.CollectorImage = c.Tracing.CollectorImage
				trSpec.RetentionHours = c.Tracing.RetentionHours
			}
			trPlan, err := TranslateTracing(ctx, cat, trSpec)
			if err != nil {
				return nil, fmt.Errorf("component %q: %w", c.Name, err)
			}
			trHCL, err := RenderTracingHCL(trPlan)
			if err != nil {
				return nil, fmt.Errorf("component %q render: %w", c.Name, err)
			}
			if trPlan.ResourceType == "kubernetes_manifest" {
				needsKubernetes = true
			}
			if trPlan.RendersHelm {
				needsHelm = true
			}
			docs = append(docs, trHCL)
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
	if rp := requiredProvidersBlock(in.Provider, needsCloudflare, needsKubernetes, needsHelm); rp != "" {
		docs = append([]string{rp}, docs...)
	}
	return docs, nil
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
func requiredProvidersBlock(provider string, needsCloudflare, needsKubernetes, needsHelm bool) string {
	src, ok := cloudProviderSource[strings.ToLower(provider)]
	cloudNonDefault := ok && !strings.HasPrefix(src[1], "hashicorp/")
	// hashicorp/kubernetes and hashicorp/helm auto-install (default namespace) but
	// once ANY required_providers entry exists, terraform requires every used
	// provider to be declared — so we only need a block when there is a non-default
	// source OR Cloudflare. When such a block IS emitted and kubernetes/helm
	// resources are present, pin them too so the block stays self-consistent.
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
	if needsKubernetes {
		b.WriteString("    kubernetes = {\n      source = \"hashicorp/kubernetes\"\n    }\n")
	}
	if needsHelm {
		// The operator-pattern CORE (upstream operator via helm_release) needs the
		// hashicorp/helm provider declared.
		b.WriteString("    helm = {\n      source = \"hashicorp/helm\"\n    }\n")
	}
	b.WriteString("  }\n}\n")
	return b.String()
}
