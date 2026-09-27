package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const dropinTmpl = "# managed by port_allocator provider; do not edit by hand\n[Service]\nEnvironment=PORT=0\n"

// --- provider ---

type portAllocatorProvider struct{}

func newProvider() provider.Provider { return &portAllocatorProvider{} }

func (p *portAllocatorProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "portallocator"
}
func (p *portAllocatorProvider) Schema(_ context.Context, _ provider.SchemaRequest, _ *provider.SchemaResponse)       {}
func (p *portAllocatorProvider) Configure(_ context.Context, _ provider.ConfigureRequest, _ *provider.ConfigureResponse) {}
func (p *portAllocatorProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{newAllocationResource}
}
func (p *portAllocatorProvider) DataSources(_ context.Context) []func() datasource.DataSource { return nil }

func main() {
	if err := providerserver.Serve(context.Background(), newProvider, providerserver.ServeOpts{
		Address: "registry.terraform.io/example/portallocator",
	}); err != nil {
		log.Fatalf("[portallocator] %v", err)
	}
}

// --- portallocator_allocation ---

type allocationResource struct{ allocator rangeAllocator }

func newAllocationResource() resource.Resource {
	return &allocationResource{allocator: rangeAllocator{start: 20000, end: 29999}}
}

type allocationModel struct {
	ID      types.String `tfsdk:"id"`
	Service types.String `tfsdk:"service"` // allocator key + systemd unit basename
	Port    types.Int64  `tfsdk:"port"`
}

func (r *allocationResource) Metadata(_ context.Context, _ resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = "portallocator_allocation"
}
func (r *allocationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{Attributes: map[string]schema.Attribute{
		"id":      schema.StringAttribute{Computed: true},
		"service": schema.StringAttribute{Required: true},
		"port":    schema.Int64Attribute{Computed: true},
	}}
}
func (r *allocationResource) Configure(_ context.Context, _ resource.ConfigureRequest, _ *resource.ConfigureResponse) {}

func (r *allocationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan allocationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	port, err := r.allocator.next()
	if err != nil {
		resp.Diagnostics.Append(diag.NewErrorDiagnostic("allocate port", err.Error()))
		return
	}
	allocs, d := readAllocations()
	if d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	if allocs.Allocations == nil {
		allocs.Allocations = map[string]int{}
	}
	allocs.Allocations[plan.Service.ValueString()] = port
	if d := writeAllocations(allocs); d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	if d := writeDropin(plan.Service.ValueString(), int64(port)); d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	plan.ID = plan.Service
	plan.Port = types.Int64Value(int64(port))
	resp.State.Set(ctx, plan)
}

func (r *allocationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state allocationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	allocs, d := readAllocations()
	if d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	service := state.Service.ValueString()
	onDisk, ok := allocs.Allocations[service]
	if !ok {
		// Entry missing from cache -> drop from state.
		resp.State.Set(ctx, allocationModel{ID: types.StringNull(), Service: state.Service})
		return
	}
	if int64(onDisk) != state.Port.ValueInt64() {
		resp.Diagnostics.Append(diag.NewWarningDiagnostic(
			"port drift",
			"allocations.json has port "+strconv.Itoa(onDisk)+
				" for "+service+", state has "+strconv.Itoa(int(state.Port.ValueInt64()))+
				"; state will win on next apply",
		))
	}
	if _, err := os.Stat(dropinPath(state.Service.ValueString())); err != nil && !os.IsNotExist(err) {
		resp.Diagnostics.Append(diag.NewErrorDiagnostic("stat dropin", err.Error()))
		return
	}
	resp.State.Set(ctx, state)
}

func (r *allocationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan allocationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if d := writeDropin(plan.Service.ValueString(), plan.Port.ValueInt64()); d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	resp.State.Set(ctx, plan)
}

func (r *allocationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state allocationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := os.Remove(dropinPath(state.Service.ValueString())); err != nil && !os.IsNotExist(err) {
		resp.Diagnostics.Append(diag.NewErrorDiagnostic("remove dropin", err.Error()))
	}
	allocs, d := readAllocations()
	if d != nil {
		resp.Diagnostics.Append(d)
		return
	}
	delete(allocs.Allocations, state.Service.ValueString())
	if d := writeAllocations(allocs); d != nil {
		resp.Diagnostics.Append(d)
	}
}

func writeDropin(unit string, port int64) diag.Diagnostic {
	dir := filepath.Join("/etc/systemd/system", unit+".service.d")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return diag.NewErrorDiagnostic("mkdir dropin dir", err.Error())
	}
	body := strings.Replace(dropinTmpl, "0", strconv.FormatInt(port, 10), 1)
	if err := os.WriteFile(dropinPath(unit), []byte(body), 0644); err != nil {
		return diag.NewErrorDiagnostic("write dropin", err.Error())
	}
	return nil
}

func dropinPath(unit string) string {
	return filepath.Join("/etc/systemd/system", unit+".service.d", "override.conf")
}
