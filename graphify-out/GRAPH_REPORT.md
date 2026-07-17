# Graph Report - .  (2026-07-17)

## Corpus Check
- cluster-only mode — file stats not available

## Summary
- 2721 nodes · 7554 edges · 129 communities (115 shown, 14 thin omitted)
- Extraction: 71% EXTRACTED · 29% INFERRED · 0% AMBIGUOUS · INFERRED: 2187 edges (avg confidence: 0.8)
- Token cost: 0 input · 0 output

## Graph Freshness
- Built from commit: `21509e7b`
- Run `git rev-parse HEAD` and compare to check if the graph is stale.
- Run `graphify update .` after code changes (no API cost).

## Community Hubs (Navigation)
- resource_topology.go
- TranslateManagedDatabase
- AssembleDOBaseline
- RegionCatalog
- ctx
- RenderObjectStorageHCL
- ProviderToCSP
- AssembleComponent
- parser.go
- importTopologyResource
- TranslateTLSCertificate
- AssembleHCL
- TranslateLoadBalancer
- scanner_test.go
- tfrunner.go
- detector_test.go
- TranslateStaticSite
- MustEmbedded
- TranslateScaleGroup
- EmbeddedCatalog
- resource_environment.go
- TranslateTracing
- subnetResourceLabel
- render.go
- migrationResource
- MonitoringPlan
- tfName
- azure_test.go
- oracle_test.go
- environmentModel
- TranslateVaultHA
- RenderSGHCL
- resource_topology_test.go
- render_macro.go
- Topology
- Select
- TranslateQueue
- VerifyNitroDocument
- platform_asgs.go
- TranslateNetwork
- TranslateVPNAccess
- alibaba_test.go
- client.go
- TranslateReservedIP
- TranslateWebService
- model.go
- HTTPClient
- doProjectDataSourceName
- TranslateSecurityGroup
- stackit_test.go
- runtime.go
- RenderKubernetesHCL
- NewHTTP
- RenderBackendDOUserData
- TranslateKeyValueStore
- RenderSSODOBootstrapUserData
- Resource
- ConfidentialContainer
- mitigation.go
- TranslateVM
- RenderSastDOBootstrapUserData
- runProgram
- render_azure.go
- TranslateImage
- RenderVPNBootstrapUserData
- opacity_test.go
- .Configure
- lib-common.sh
- flatEnvironmentComponentAttributes
- RenderCacheHCL
- virtualmachine.go
- RenderSSOBootstrapUserData
- TestUbicloudUnsupportedComponents
- TestProviderInterfaces
- TranslateEmail
- RenderMcpDOUserData
- DeriveSecurityBaseline
- SupabaseMappingEntry
- clientCredentialsSource
- VaultKVDataSourceHCL
- ephemeralKey
- .assembleInputFromModel
- resource_environment_test.go
- import_surfaces_schema_test.go
- RenderSecretsHCL
- RenderServerlessHCL
- RenderWAFHCL
- render_ibm.go
- BackendCatalog
- http_client.go
- TestEngineBindsEphemeralPubKeyBeforeAttest
- NewMigrationResource
- TranslateKubernetes
- PlatformScaleGroupComponents
- render_alibaba.go
- VaultBootFetchSnippet
- rule_test.go
- TranslateAttachToExistingALB
- TranslateBlockStorage
- verify-migration.sh
- roundtrip.sh
- roundtrip.sh
- roundtrip.sh
- roundtrip.sh
- scanner_test.go
- roundtrip.sh
- roundtrip.sh
- roundtrip.sh
- TestCatalogSizingMatrix
- nitro_nsm_linux.go
- gen.sh
- roundtrip.sh
- add-api-prod-route.sh
- add-staging-fe-route.sh
- roundtrip.sh
- roundtrip.sh
- roundtrip.sh
- roundtrip.sh
- roundtrip.sh
- cutover-db.sh
- pg-dump-restore.sh
- pg-logical-replication-setup.sh
- github.com/PyxCloud/terraform-provider-pyxcloud

## God Nodes (most connected - your core abstractions)
1. `MustEmbedded()` - 422 edges
2. `tfName()` - 182 edges
3. `AssembleHCL()` - 159 edges
4. `NewEmbedded()` - 57 edges
5. `AssembleComponent` - 55 edges
6. `ctx()` - 52 edges
7. `ProviderToCSP()` - 47 edges
8. `RegionCatalog` - 44 edges
9. `lc()` - 44 edges
10. `TranslateLoadBalancer()` - 42 edges

## Surprising Connections (you probably didn't know these)
- `main()` --calls--> `MustEmbedded()`  [INFERRED]
  cmd/pyxnet-render/main.go → internal/catalog/embedded.go
- `renderCache()` --calls--> `TranslateCache()`  [INFERRED]
  cmd/pyxnet-render/main.go → internal/catalog/cache.go
- `renderCache()` --calls--> `RenderCacheHCL()`  [INFERRED]
  cmd/pyxnet-render/main.go → internal/catalog/render_macro.go
- `renderQueue()` --calls--> `TranslateQueue()`  [INFERRED]
  cmd/pyxnet-render/main.go → internal/catalog/messaging.go
- `renderQueue()` --calls--> `RenderMessagingHCL()`  [INFERRED]
  cmd/pyxnet-render/main.go → internal/catalog/render_macro.go

## Import Cycles
- None detected.

## Communities (129 total, 14 thin omitted)

### Community 0 - "resource_topology.go"
Cohesion: 0.05
Nodes (80): Component, compareComponentsFromModel(), costToModel(), Bool, Client, ConfigureRequest, ConfigureResponse, Context (+72 more)

### Community 1 - "TranslateManagedDatabase"
Cohesion: 0.06
Nodes (80): DataSafetyViolation, ErrDataSafetyForceReplace, ErrDBClassNotFound, ManagedDatabasePlan, ManagedDatabaseSpec, MDBCatalog, OVHCatalog, ovhFlavorRow (+72 more)

### Community 2 - "AssembleDOBaseline"
Cohesion: 0.06
Nodes (79): DOBaselineOptions, DOBaselineSecrets, DOBaselineService, doEdgeOrigin, EdgeTLSTerminator, StagingFEDOBootstrapSpec, VaultDropletSpec, VaultSealMode (+71 more)

### Community 3 - "RegionCatalog"
Cohesion: 0.07
Nodes (71): Catalog, ErrRegionNotFound, PipelineControlPlanePlan, PipelineControlPlaneSpec, RegionCatalog, VMCatalog, emit(), fatal() (+63 more)

### Community 4 - "ctx"
Cohesion: 0.07
Nodes (68): CacheSpec, CDNSpec, DNSZoneSpec, ErrComponentUnsupported, SecretsSpec, ServerlessSpec, WAFSpec, cacheNodeClass() (+60 more)

### Community 5 - "RenderObjectStorageHCL"
Cohesion: 0.07
Nodes (68): AccessLogConfig, AssembleObjectStorage, ContainerRegistryPlan, ContainerRegistrySpec, LifecycleRule, ObjectStoragePlan, ObjectStorageSpec, SSEConfig (+60 more)

### Community 6 - "ProviderToCSP"
Cohesion: 0.06
Nodes (62): IAMPolicy, IAMSpec, KMSPlan, KMSSpec, SyntheticsPlan, SyntheticsSpec, WorkloadIdentityPlan, WorkloadIdentitySpec (+54 more)

### Community 7 - "AssembleComponent"
Cohesion: 0.06
Nodes (60): AssembleAttachToExistingALB, AssembleBlockStorage, AssembleCache, AssembleCDN, AssembleComponent, AssembleContainerRegistry, AssembleDNS, AssembleEmail (+52 more)

### Community 8 - "parser.go"
Cohesion: 0.06
Nodes (51): PolicyVerdict, DriftEvent, mockClient, EvaluateDriftPolicy(), EvaluateDriftPolicyWithCost(), EvaluateDriftPolicyWithSecScan(), isSecuritySensitive(), T (+43 more)

### Community 9 - "importTopologyResource"
Cohesion: 0.05
Nodes (44): Diagnostic, Diagnostics, Bool, Client, ConfigureRequest, ConfigureResponse, Context, Map (+36 more)

### Community 10 - "TranslateTLSCertificate"
Cohesion: 0.08
Nodes (48): ScheduledTriggerPlan, ScheduledTriggerSpec, TLSCertificatePlan, TLSCertificateSpec, quoteList(), renderScheduledTriggerAWS(), renderScheduledTriggerDO(), RenderScheduledTriggerHCL() (+40 more)

### Community 11 - "AssembleHCL"
Cohesion: 0.12
Nodes (49): ArchitectureFinding, main(), DetectArchitectureMismatches(), AssembleHCL(), T, TestAssembleHCLAttachToExistingALB(), TestAssembleHCLAWSVMEnv(), TestAssembleHCLB4SecretsVaultAutoAlias() (+41 more)

### Community 12 - "TranslateLoadBalancer"
Cohesion: 0.11
Nodes (45): LBHealthCheckPlan, LBHealthCheckSpec, LBListenerPlan, LBListenerSpec, LBRoutingRule, LBRoutingRulePlan, LoadBalancerPlan, LoadBalancerSpec (+37 more)

### Community 13 - "scanner_test.go"
Cohesion: 0.10
Nodes (37): Input, Result, Scanner, CostBlowoutRule, CostBlowoutRuleType, CostBlowoutSignal, Detector, Default() (+29 more)

### Community 14 - "tfrunner.go"
Cohesion: 0.11
Nodes (35): awsOutput(), awsRevokeSGRule(), awsSecurityGroupID(), awsSecurityGroupRuleImportCandidates(), awsSGRuleImportID(), describeSGRules(), desiredSGRuleKeys(), discoverAWSImportCandidates() (+27 more)

### Community 15 - "detector_test.go"
Cohesion: 0.13
Nodes (36): Detector, Finding, Module, Rule, RuleType, Finding, DefaultRules(), distinctCount() (+28 more)

### Community 16 - "TranslateStaticSite"
Cohesion: 0.09
Nodes (40): CloudflareCDNPlan, CloudflareCDNSpec, CloudflareDNSPlan, CloudflareDNSSpec, DNSRecord, StaticSitePlan, StaticSiteSpec, T (+32 more)

### Community 17 - "MustEmbedded"
Cohesion: 0.12
Nodes (43): MustEmbedded(), T, TestIBMCache(), TestIBMDNSZonePrivateAndPublic(), TestIBMEventStreaming(), TestIBMKubernetes(), TestIBMLoadBalancer(), TestIBMManagedDatabase() (+35 more)

### Community 18 - "TranslateScaleGroup"
Cohesion: 0.14
Nodes (33): ErrAutoscaleUnsupported, ScaleGroupPlan, ScaleGroupSpec, renderASGAlibaba(), renderASGIBM(), renderASGGCP(), RenderScaleGroupHCL(), canonicalHealth() (+25 more)

### Community 19 - "EmbeddedCatalog"
Cohesion: 0.13
Nodes (22): awsInstanceTypeOfferings, MDBRow, OSImageRow, RegionRow, EmbeddedCatalog, Context, key(), mdbRegionEngineKey() (+14 more)

### Community 20 - "resource_environment.go"
Cohesion: 0.16
Nodes (34): Bool, Float64, Int64, String, envAlarmModel, envAttachToExistingALBModel, envBlockStorageModel, envCacheModel (+26 more)

### Community 21 - "TranslateTracing"
Cohesion: 0.13
Nodes (30): HelmReleaseSpec, HelmSet, ManifestCR, TracingPlan, TracingSpec, renderClusterDataSource(), renderHelmRelease(), renderOperatorComponent() (+22 more)

### Community 22 - "subnetResourceLabel"
Cohesion: 0.13
Nodes (31): VMPlan, renderVMAlibaba(), renderVMIBM(), renderVMLinode(), hcOCIProto(), lbOCIProto(), ociADDataSource(), ociADRef() (+23 more)

### Community 23 - "render.go"
Cohesion: 0.11
Nodes (31): AccessPolicyPlan, IAMPlan, asgResourceLabel(), awsHealthCheckType(), distinctRuleTargetNames(), doProto(), doScaleGroupTag(), gcpAccountID() (+23 more)

### Community 24 - "migrationResource"
Cohesion: 0.09
Nodes (23): Bool, Config, ConfigureRequest, ConfigureResponse, Context, CreateRequest, CreateResponse, DeleteRequest (+15 more)

### Community 25 - "MonitoringPlan"
Cohesion: 0.15
Nodes (28): AssembleMonitoring, LogGroup, MetricAlarm, MonitoringPlan, MonitoringSpec, ScrapeTarget, Context, T (+20 more)

### Community 26 - "tfName"
Cohesion: 0.21
Nodes (24): DNSZonePlan, NetworkPlan, renderAlibaba(), renderDNSAlibaba(), renderDNSZoneAzure(), renderNetworkAzure(), renderDNSIBM(), renderNetworkIBM() (+16 more)

### Community 27 - "azure_test.go"
Cohesion: 0.17
Nodes (30): azureCtx(), Context, T, TestAzureDeterministicRender(), TestAzureMDBDataSafetyGuard(), TestAzureProviderEnabled(), TestAzureRenderCache(), TestAzureRenderCDN() (+22 more)

### Community 28 - "oracle_test.go"
Cohesion: 0.17
Nodes (29): T, mustContain(), oracleMDB(), TestOracleCDNUnsupported(), TestOracleImageResolution(), TestOracleMDBDataSafetyGuard(), TestOracleObjectStoragePublicIsOptIn(), TestOracleProviderToCSP() (+21 more)

### Community 29 - "environmentModel"
Cohesion: 0.12
Nodes (20): Client, ConfigureRequest, ConfigureResponse, Context, CreateRequest, CreateResponse, DeleteRequest, DeleteResponse (+12 more)

### Community 30 - "TranslateVaultHA"
Cohesion: 0.17
Nodes (25): VaultHAPlan, VaultHASpec, renderVaultAuthGlobalManifest(), renderVaultConnectionManifest(), renderVaultHAAWS(), renderVaultHADO(), RenderVaultHAHCL(), vaultRaftConfig() (+17 more)

### Community 31 - "RenderSGHCL"
Cohesion: 0.13
Nodes (29): RulePlan, SecurityGroupPlan, SecurityGroupSpec, SecurityRule, awsProto(), renderSGAzure(), dedupeCIDRs(), Builder (+21 more)

### Community 32 - "resource_topology_test.go"
Cohesion: 0.14
Nodes (26): NewTopologyResource(), T, TestResourceDataSafetyGuardWiring(), TestResourceTranslateLoadBalancer(), TestResourceTranslateLoadBalancerNil(), TestResourceTranslateManagedDatabase(), TestResourceTranslateManagedDatabaseNil(), TestResourceTranslateManagedDatabaseTestOverride() (+18 more)

### Community 33 - "render_macro.go"
Cohesion: 0.17
Nodes (23): CDNPlan, MessagingPlan, renderCDNAlibaba(), renderQueueAlibaba(), renderStreamAlibaba(), renderCDNAzure(), renderMessagingAzure(), renderStreamIBM() (+15 more)

### Community 34 - "Topology"
Cohesion: 0.15
Nodes (14): Candidate, Client, compareRequest, Config, StubClient, Topology, Context, Mutex (+6 more)

### Community 35 - "Select"
Cohesion: 0.19
Nodes (22): NewConfidentialContainer(), autoDetect(), DetectAll(), Select(), contains(), Sealed, T, noopOpener() (+14 more)

### Community 36 - "TranslateQueue"
Cohesion: 0.16
Nodes (23): MessagingKind, QueueSpec, StreamSpec, CanonicalQueueType(), CanonicalStreamType(), Context, T, TestAssembleHCLDOQueueOperatorPinsHelm() (+15 more)

### Community 37 - "VerifyNitroDocument"
Cohesion: 0.19
Nodes (21): Certificate, bytesEqualConstTime(), CertPool, Time, ParseNitroDocument(), CertPool, T, Time (+13 more)

### Community 38 - "platform_asgs.go"
Cohesion: 0.19
Nodes (19): DOBootstrapSpecs, OBSDOBootstrapSpec, PlatformBootstraps, PlatformBootstrapsByProvider, PlatformService, PlatformBootstrapsWithOBSDO(), PlatformScaleGroupComponentsWithBootstrap(), PlatformScaleGroupComponentsWithBootstraps() (+11 more)

### Community 39 - "TranslateNetwork"
Cohesion: 0.17
Nodes (21): NetworkSpec, SubnetPlan, deriveZones(), Context, T, TestTranslateNetworkAWS(), TestTranslateNetworkDO(), TestTranslateNetworkGCP() (+13 more)

### Community 40 - "TranslateVPNAccess"
Cohesion: 0.19
Nodes (21): VPNAccessPlan, VPNAccessSpec, renderVPNAccessAWS(), RenderVPNAccessHCL(), CanonicalVPNAccessType(), Context, T, TestCanonicalVPNAccessType() (+13 more)

### Community 41 - "alibaba_test.go"
Cohesion: 0.16
Nodes (23): T, TestAlibabaCache(), TestAlibabaCDN(), TestAlibabaDNSZonePublicAndPrivateUnsupported(), TestAlibabaKubernetes(), TestAlibabaLoadBalancer(), TestAlibabaManagedDatabase(), TestAlibabaManagedDatabaseDataSafetyGuard() (+15 more)

### Community 42 - "client.go"
Cohesion: 0.12
Nodes (24): Client, Context, RawMessage, Sealed, NewClient(), redactBackendErrorBody(), redactJSON(), redactJSONValue() (+16 more)

### Community 43 - "TranslateReservedIP"
Cohesion: 0.19
Nodes (20): ReservedIPPlan, ReservedIPSpec, renderReservedIPAWS(), renderReservedIPDO(), renderReservedIPGCP(), RenderReservedIPHCL(), CanonicalReservedIPType(), Context (+12 more)

### Community 44 - "TranslateWebService"
Cohesion: 0.21
Nodes (18): WebServicePlan, WebServiceSpec, renderWebServiceDO(), RenderWebServiceHCL(), CanonicalWebServiceType(), Context, sortedEnvKeys(), T (+10 more)

### Community 45 - "model.go"
Cohesion: 0.14
Nodes (10): FeeRequiredError, ImportDiscoveryRequest, ImportDiscoveryResponse, ImportTopologyRequest, ImportTopologyResponse, TranslateResult, VMType, newFeeRequiredError() (+2 more)

### Community 46 - "HTTPClient"
Cohesion: 0.31
Nodes (6): HTTPClient, apiError(), componentsToCanonical(), Client, Context, Response

### Community 47 - "doProjectDataSourceName"
Cohesion: 0.21
Nodes (16): doProjectDataSourceName(), RenderDOProjectDataSource(), RenderDOProjectResources(), T, TestFullEstateDO_ProjectPlacementWiring(), TestFullEstateDO_ProjectResourcesBinding(), TestRenderDOProjectDataSource(), TestRenderDOProjectResources() (+8 more)

### Community 48 - "TranslateSecurityGroup"
Cohesion: 0.23
Nodes (18): Context, T, TestAWSFirewallPrefixListReference(), TestDOFirewallPrefixListInlined(), TestDOFirewallSourceSGRendersAsTags(), TestPrefixListReferenceUndefinedIsHardError(), T, TestExternalSourceSGIDRuleRendersLiteral() (+10 more)

### Community 49 - "stackit_test.go"
Cohesion: 0.26
Nodes (19): Context, T, stackitCtx(), TestStackItMDBDataSafetyGuard(), TestStackItProviderToCSP(), TestStackItRenderDNSZone(), TestStackItRenderKubernetes(), TestStackItRenderLoadBalancer() (+11 more)

### Community 50 - "runtime.go"
Cohesion: 0.16
Nodes (14): Client, Context, Sealed, NewEngine(), opener(), Engine, Options, Result (+6 more)

### Community 51 - "RenderKubernetesHCL"
Cohesion: 0.38
Nodes (10): K8sPlan, renderK8sAlibaba(), renderKubernetesAzure(), renderK8sLinode(), renderK8sAWS(), renderK8sDO(), renderK8sGCP(), RenderKubernetesHCL() (+2 more)

### Community 52 - "NewHTTP"
Cohesion: 0.24
Nodes (17): Config, NewHTTP(), T, sampleTopology(), TestComponentsCanonicalRoundTrip(), TestHTTPClientCanonicalShape(), TestHTTPClientCompareMapsResults(), TestHTTPClientGetNotFound() (+9 more)

### Community 53 - "RenderBackendDOUserData"
Cohesion: 0.25
Nodes (14): BackendBootstrapSpec, BackendDOScaleGroupComponent(), RenderBackendDOUserData(), T, TestBackendBootstrapVariableNamesPartitioned(), TestBackendDOScaleGroupComponentWiresDOUserData(), TestBackendDOTerraformValidate(), TestRenderBackendDOAWSCouplingsAdapted() (+6 more)

### Community 54 - "TranslateKeyValueStore"
Cohesion: 0.23
Nodes (15): KeyValueStorePlan, KeyValueStoreSpec, CanonicalKeyValueStoreType(), Context, T, TestCanonicalKeyValueStoreType(), TestKeyValueStoreAWSDefaultPartitionKey(), TestKeyValueStoreUnsupportedProvider() (+7 more)

### Community 55 - "RenderSSODOBootstrapUserData"
Cohesion: 0.25
Nodes (13): SSODOBootstrapSpec, RenderSSODOBootstrapUserData(), T, TestRenderSSODOBootstrapFaithfulPort(), TestRenderSSODOBootstrapRequiresInjectedSecrets(), TestSSODOUserDataRendersOnDigitalOcean(), TestSSODOUserDataVaultDataSourceRefs(), TestSSODOVaultDataSources() (+5 more)

### Community 56 - "Resource"
Cohesion: 0.31
Nodes (17): Finding, Resource, ScanInput, ScanResult, checkIAMWildcards(), checkOpenPorts(), checkPublicStorage(), checkUnencryptedStorage() (+9 more)

### Community 57 - "ConfidentialContainer"
Cohesion: 0.15
Nodes (9): ccMeasurement(), Context, Context, hwTEEMeasurement(), Sealed, ConfidentialContainer, Evidence, SealedInputs (+1 more)

### Community 58 - "mitigation.go"
Cohesion: 0.24
Nodes (15): selfHostRecipe, T, TestAssembleHCLFallbackServicesSelfHostOnVM(), TestAssembleHCLNativeSupportDoesNotFallback(), TestNativeSupportMatchesRendererSurface(), TestNonMitigatablePolicyComponentsStayNativeRouted(), Context, Mitigatable() (+7 more)

### Community 59 - "TranslateVM"
Cohesion: 0.29
Nodes (16): Context, T, TestRenderVMAWS(), TestRenderVMDO(), TestRenderVMGCP(), TestSubnetResourceLabel(), TestTranslateVMAWS(), TestTranslateVMAWSArm64() (+8 more)

### Community 60 - "RenderSastDOBootstrapUserData"
Cohesion: 0.23
Nodes (11): SastDOBootstrapSpec, RenderSastDOBootstrapUserData(), T, TestPlatformProviderBootstrapWiresSastDO(), TestRenderSastDOBootstrapFaithfulPort(), TestRenderSastDOBootstrapInlinesNoSecretValues(), TestRenderSastDOBootstrapRequiresEnvironment(), TestSastDOScaleDownFloorIsOne() (+3 more)

### Community 61 - "runProgram"
Cohesion: 0.21
Nodes (12): convergeFailed(), decodeBundle(), Context, RawMessage, pct(), runProgram(), Context, sealedWASMMeasurement() (+4 more)

### Community 62 - "render_azure.go"
Cohesion: 0.25
Nodes (14): azureFlexSKU(), azureFlexStorageMB(), azureLBProbeProto(), azureProto(), azureRGRef(), azureStorageAccountName(), mdbAzureMySQLVersion(), mdbAzurePostgresVersion() (+6 more)

### Community 63 - "TranslateImage"
Cohesion: 0.32
Nodes (12): ImageKind, ImageRef, classifyImageKind(), Context, RenderImageRefHCL(), T, TestClassifyAndRenderSSMParameter(), TestTranslateImageAWSLiteral() (+4 more)

### Community 64 - "RenderVPNBootstrapUserData"
Cohesion: 0.27
Nodes (9): VPNBootstrapSpec, escapeBashExpansionsForHeredoc(), RenderVPNBootstrapUserData(), T, TestPlatformBootstrapWiresVPNUserDataByProvider(), TestRenderVPNBootstrapDeterministic(), TestRenderVPNBootstrapFaithfulReArch(), TestRenderVPNBootstrapInjectsIdentityFromVars() (+1 more)

### Community 65 - "opacity_test.go"
Cohesion: 0.48
Nodes (13): newEphemeralKey(), driveRuntime(), genericSteps(), Sealed, T, localMeasurement(), sealBundleForTest(), TestOpacity_DryRunNoCutover() (+5 more)

### Community 66 - ".Configure"
Cohesion: 0.19
Nodes (9): ConfigureRequest, ConfigureResponse, Context, DataSource, MetadataRequest, MetadataResponse, SchemaRequest, SchemaResponse (+1 more)

### Community 67 - "lib-common.sh"
Cohesion: 0.16
Nodes (4): die(), require_cmd(), require_env(), lib-common.sh script

### Community 68 - "flatEnvironmentComponentAttributes"
Cohesion: 0.23
Nodes (12): Attribute, alarmsAttribute(), dnsRecordsAttribute(), flatEnvironmentComponentAttributes(), ListNestedAttribute, SchemaRequest, SchemaResponse, inlinePolicyAttribute() (+4 more)

### Community 69 - "RenderCacheHCL"
Cohesion: 0.26
Nodes (12): CachePlan, renderCacheAlibaba(), azureRedisSizing(), renderCacheAzure(), renderCacheIBM(), renderCacheAWS(), renderCacheDO(), renderCacheGCP() (+4 more)

### Community 70 - "virtualmachine.go"
Cohesion: 0.21
Nodes (8): ErrOSImageNotFound, ErrSKUNotFound, VMInstancePlan, VMRow, VMSpec, familyRank(), nearestSizes(), validateVMSpec()

### Community 71 - "RenderSSOBootstrapUserData"
Cohesion: 0.29
Nodes (9): SSOBootstrapSpec, RenderSSOBootstrapUserData(), T, TestPlatformBootstrapWiresSSOUserData(), TestPlatformScaleGroupComponentsBackwardCompatible(), TestRenderSSOBootstrapFaithfulPort(), TestRenderSSOBootstrapInlinesNoSecretValues(), TestRenderSSOBootstrapRequiresEnvironment() (+1 more)

### Community 72 - "TestUbicloudUnsupportedComponents"
Cohesion: 0.28
Nodes (12): T, TestUbicloudCatalogResolvesRegion(), TestUbicloudCatalogResolvesSKUAndImage(), TestUbicloudManagedDatabaseMySQLUnsupported(), TestUbicloudManagedPostgresRender(), TestUbicloudNetworkRender(), TestUbicloudProviderRegistered(), TestUbicloudSecurityGroupRender() (+4 more)

### Community 73 - "TestProviderInterfaces"
Cohesion: 0.20
Nodes (12): DataSource, NewCompareDataSource(), T, TestCompareSchemaUsesPyxTypedComponentBlocks(), TestProviderRegistersImportSurfaces(), New(), T, TestDataSourceSchema() (+4 more)

### Community 74 - "TranslateEmail"
Cohesion: 0.35
Nodes (10): EmailPlan, EmailSpec, Context, RenderEmailHCL(), renderEmailRelayHCL(), T, TestTranslateEmailAWSSES(), TestTranslateEmailDONoHardError() (+2 more)

### Community 75 - "RenderMcpDOUserData"
Cohesion: 0.30
Nodes (7): McpDOBootstrapSpec, McpDOScaleGroupComponent(), RenderMcpDOUserData(), T, TestMcpDOVaultDataSources(), TestRenderMcpDOInlinesNoSecretValues(), TestRenderMcpDOListensViaHTTPPortEnv()

### Community 76 - "DeriveSecurityBaseline"
Cohesion: 0.29
Nodes (10): SecurityBaseline, baselineEgress(), boolPtr(), DeriveSecurityBaseline(), T, itoaPort(), TestAssembleHCLSecurityBaselineEgressLockdown(), TestAssembleHCLSecurityBaselineSecretsRecoverable() (+2 more)

### Community 77 - "SupabaseMappingEntry"
Cohesion: 0.32
Nodes (10): SupabaseMappingEntry, SupabaseServiceKind, LookupSupabaseService(), SupabaseAbsorbedComponents(), SupabaseCanonicalComponents(), T, TestLookupSupabaseService_byImage(), TestLookupSupabaseService_byName() (+2 more)

### Community 78 - "clientCredentialsSource"
Cohesion: 0.20
Nodes (9): clientCredentialsSource, staticToken, tokenSource, Duration, Client, Context, Mutex, Time (+1 more)

### Community 79 - "VaultKVDataSourceHCL"
Cohesion: 0.27
Nodes (10): KnownVaultKVPaths(), T, TestVaultKVDataSourceHCLShape(), TestVaultKVRefShape(), TestVaultProviderBlockShape(), VaultKVDataSourceHCL(), VaultKVDataSourceLabel(), vaultKVLabelsReferenced() (+2 more)

### Community 80 - "ephemeralKey"
Cohesion: 0.26
Nodes (6): zeroize(), deriveKey(), open(), sealTo(), ephemeralKey, Sealed

### Community 81 - ".assembleInputFromModel"
Cohesion: 0.27
Nodes (11): boolSet(), environmentComponentsFromModel(), envMapFromModel(), Map, hasDatabaseFields(), hasScaleGroupFields(), intFromString(), intSet() (+3 more)

### Community 82 - "resource_environment_test.go"
Cohesion: 0.32
Nodes (11): NewEnvironmentResource(), T, TestEnvironmentAccessPolicyCanRequestInstanceProfile(), TestEnvironmentAssembleInputMapsVaultHABlock(), TestEnvironmentAssembleInputOmitsVaultHAWhenUnset(), TestEnvironmentAssembleInputUsesFlatComponentFields(), TestEnvironmentModeSelector(), TestEnvironmentSchemaHasDualModeSelector() (+3 more)

### Community 83 - "import_surfaces_schema_test.go"
Cohesion: 0.16
Nodes (10): DataSource, NewImportDiscoveryDataSource(), contains(), containsAll(), T, TestImportDiscoverySchema(), TestImportTopologyFeeRequiredDiagnosticMessage(), TestImportTopologySchemaAvoidsRawCredentialState() (+2 more)

### Community 84 - "RenderSecretsHCL"
Cohesion: 0.18
Nodes (19): SecretsPlan, renderSecretsAlibaba(), azureKeyVaultName(), renderSecretsAzure(), renderSecretsIBM(), renderSecretsAWS(), renderSecretsGCP(), RenderSecretsHCL() (+11 more)

### Community 85 - "RenderServerlessHCL"
Cohesion: 0.36
Nodes (10): ServerlessPlan, renderServerlessAlibaba(), azureFunctionStack(), renderServerlessAzure(), renderServerlessIBM(), renderServerlessAWS(), renderServerlessDO(), renderServerlessGCP() (+2 more)

### Community 86 - "RenderWAFHCL"
Cohesion: 0.36
Nodes (10): WAFPlan, renderWAFAlibaba(), azureWAFPolicyName(), renderWAFAzure(), renderWAFIBM(), renderWAFAWS(), renderWAFCloudflare(), renderWAFGCP() (+2 more)

### Community 87 - "render_ibm.go"
Cohesion: 0.33
Nodes (9): ibmSGDirection(), lbIBMProto(), maxInt(), mdbIBMService(), renderK8sIBM(), renderLBIBM(), renderMDBIBM(), renderObjectStorageIBM() (+1 more)

### Community 88 - "BackendCatalog"
Cohesion: 0.33
Nodes (4): BackendCatalog, EmbeddedCatalog, Context, NewBackend()

### Community 89 - "http_client.go"
Cohesion: 0.31
Nodes (7): CandidateCost, compareResponse, topologyWire, translateRequest, asInt(), asString(), canonicalToComponents()

### Community 90 - "TestEngineBindsEphemeralPubKeyBeforeAttest"
Cohesion: 0.31
Nodes (4): Context, T, TestEngineBindsEphemeralPubKeyBeforeAttest(), bindingRuntime

### Community 91 - "NewMigrationResource"
Cohesion: 0.32
Nodes (5): NewMigrationResource(), T, TestMigrationDisabledIsNoOp(), TestMigrationResourceSchema(), testDiags

### Community 92 - "TranslateKubernetes"
Cohesion: 0.43
Nodes (6): K8sSpec, CanonicalKubernetesType(), Context, TranslateKubernetes(), validateK8sSpec(), normalizeBounds()

### Community 93 - "PlatformScaleGroupComponents"
Cohesion: 0.52
Nodes (6): PlatformScaleGroupComponents(), T, TestPlatformASGsRoundTripAWS(), TestPlatformASGsRoundTripDO(), TestPlatformScaleGroupComponentsAreScaleGroupsOfOne(), TestPlatformServicesCanonicalShape()

### Community 94 - "render_alibaba.go"
Cohesion: 0.33
Nodes (8): alibabaPortRange(), alibabaProto(), lbAlibabaProto(), mdbAlibabaEngine(), renderLBAlibaba(), renderMDBAlibaba(), renderObjectStorageAlibaba(), renderSGAlibaba()

### Community 95 - "VaultBootFetchSnippet"
Cohesion: 0.48
Nodes (5): T, TestVaultBootFetchSnippetNoHardcodedCreds(), TestVaultBootFetchSnippetReusableForDifferentLeaves(), TestVaultBootFetchSnippetShape(), VaultBootFetchSnippet()

### Community 96 - "rule_test.go"
Cohesion: 0.48
Nodes (6): T, TestDefaultBudgetOverrunRule(), TestDefaultCostSpikeRule(), TestDefaultResourceCostAnomalyRule(), TestDisabledRule(), TestRuleWindow()

### Community 97 - "TranslateAttachToExistingALB"
Cohesion: 0.53
Nodes (5): AttachToExistingALBPlan, AttachToExistingALBSpec, Context, RenderAttachToExistingALBHCL(), TranslateAttachToExistingALB()

### Community 98 - "TranslateBlockStorage"
Cohesion: 0.53
Nodes (5): BlockStoragePlan, BlockStorageSpec, Context, RenderBlockStorageHCL(), TranslateBlockStorage()

### Community 101 - "verify-migration.sh"
Cohesion: 0.53
Nodes (3): check_lag(), fail(), verify-migration.sh script

### Community 102 - "roundtrip.sh"
Cohesion: 0.60
Nodes (3): gen(), plan(), roundtrip.sh script

### Community 103 - "roundtrip.sh"
Cohesion: 0.70
Nodes (4): apply_destroy(), gen(), plan(), roundtrip.sh script

### Community 104 - "roundtrip.sh"
Cohesion: 0.70
Nodes (4): apply_destroy(), gen(), plan(), roundtrip.sh script

### Community 105 - "roundtrip.sh"
Cohesion: 0.70
Nodes (4): apply_destroy(), gen(), plan(), roundtrip.sh script

### Community 106 - "scanner_test.go"
Cohesion: 0.60
Nodes (4): T, TestScan_IAMWildcards(), TestScan_OpenPorts(), TestScan_PublicStorage()

### Community 107 - "roundtrip.sh"
Cohesion: 0.83
Nodes (3): gen(), plan(), roundtrip.sh script

### Community 108 - "roundtrip.sh"
Cohesion: 0.83
Nodes (3): gen(), plan(), roundtrip.sh script

### Community 109 - "roundtrip.sh"
Cohesion: 0.83
Nodes (3): gen(), plan(), roundtrip.sh script

### Community 110 - "TestCatalogSizingMatrix"
Cohesion: 0.67
Nodes (3): T, TestCatalogSizingMatrix(), TestResolveSKUJITAWS()

## Knowledge Gaps
- **22 isolated node(s):** `gen.sh script`, `roundtrip.sh script`, `add-api-prod-route.sh script`, `add-staging-fe-route.sh script`, `roundtrip.sh script` (+17 more)
  These have ≤1 connection - possible missing edges or undocumented components.
- **14 thin communities (<3 nodes) omitted from report** — run `graphify query` to explore isolated nodes.

## Suggested Questions
_Questions this graph is uniquely positioned to answer:_

- **Why does `AssembleHCL()` connect `AssembleHCL` to `TranslateManagedDatabase`, `AssembleDOBaseline`, `RegionCatalog`, `ctx`, `RenderObjectStorageHCL`, `ProviderToCSP`, `AssembleComponent`, `TranslateTLSCertificate`, `TranslateLoadBalancer`, `TranslateStaticSite`, `TranslateScaleGroup`, `EmbeddedCatalog`, `TranslateTracing`, `subnetResourceLabel`, `render.go`, `MonitoringPlan`, `tfName`, `environmentModel`, `TranslateVaultHA`, `RenderSGHCL`, `render_macro.go`, `TranslateQueue`, `platform_asgs.go`, `TranslateNetwork`, `TranslateVPNAccess`, `TranslateReservedIP`, `TranslateWebService`, `doProjectDataSourceName`, `TranslateSecurityGroup`, `RenderKubernetesHCL`, `RenderBackendDOUserData`, `TranslateKeyValueStore`, `RenderSSODOBootstrapUserData`, `mitigation.go`, `TranslateVM`, `RenderSastDOBootstrapUserData`, `RenderCacheHCL`, `TranslateEmail`, `DeriveSecurityBaseline`, `VaultKVDataSourceHCL`, `RenderSecretsHCL`, `RenderServerlessHCL`, `RenderWAFHCL`, `TranslateKubernetes`, `PlatformScaleGroupComponents`, `TranslateAttachToExistingALB`, `TranslateBlockStorage`?**
  _High betweenness centrality (0.301) - this node is a cross-community bridge._
- **Why does `MustEmbedded()` connect `MustEmbedded` to `TranslateManagedDatabase`, `AssembleDOBaseline`, `RegionCatalog`, `ctx`, `RenderObjectStorageHCL`, `ProviderToCSP`, `AssembleComponent`, `TranslateTLSCertificate`, `AssembleHCL`, `TranslateLoadBalancer`, `TranslateStaticSite`, `TranslateScaleGroup`, `EmbeddedCatalog`, `TranslateTracing`, `azure_test.go`, `oracle_test.go`, `TranslateVaultHA`, `resource_topology_test.go`, `TranslateQueue`, `platform_asgs.go`, `TranslateNetwork`, `TranslateVPNAccess`, `alibaba_test.go`, `TranslateReservedIP`, `TranslateWebService`, `doProjectDataSourceName`, `TranslateSecurityGroup`, `stackit_test.go`, `RenderBackendDOUserData`, `TranslateKeyValueStore`, `RenderSSODOBootstrapUserData`, `TranslateVM`, `RenderSastDOBootstrapUserData`, `TranslateImage`, `TestUbicloudUnsupportedComponents`, `TranslateEmail`, `DeriveSecurityBaseline`, `PlatformScaleGroupComponents`, `TestCatalogSizingMatrix`?**
  _High betweenness centrality (0.261) - this node is a cross-community bridge._
- **Why does `emit()` connect `RegionCatalog` to `runProgram`, `render_ibm.go`?**
  _High betweenness centrality (0.085) - this node is a cross-community bridge._
- **Are the 419 inferred relationships involving `MustEmbedded()` (e.g. with `main()` and `run()`) actually correct?**
  _`MustEmbedded()` has 419 INFERRED edges - model-reasoned connections that need verification._
- **Are the 143 inferred relationships involving `tfName()` (e.g. with `RenderAttachToExistingALBHCL()` and `RenderBlockStorageHCL()`) actually correct?**
  _`tfName()` has 143 INFERRED edges - model-reasoned connections that need verification._
- **Are the 151 inferred relationships involving `AssembleHCL()` (e.g. with `main()` and `RenderAttachToExistingALBHCL()`) actually correct?**
  _`AssembleHCL()` has 151 INFERRED edges - model-reasoned connections that need verification._
- **Are the 43 inferred relationships involving `NewEmbedded()` (e.g. with `main()` and `TestAssembleHCLAttachToExistingALB()`) actually correct?**
  _`NewEmbedded()` has 43 INFERRED edges - model-reasoned connections that need verification._