package domain

import "testing"

func TestDeviceRules(t *testing.T) {
	valid := DeviceRegistration{OwnerType: Customer, OwnerID: 7, RegisteredBy: 8, Platform: "IOS", InstallationID: "private-installation"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*DeviceRegistration){func(d *DeviceRegistration) { d.OwnerID = 0 }, func(d *DeviceRegistration) { d.OwnerType = "ADMIN" }, func(d *DeviceRegistration) { d.RegisteredBy = 0 }, func(d *DeviceRegistration) { d.Platform = "OTHER" }, func(d *DeviceRegistration) { d.InstallationID = "short" }, func(d *DeviceRegistration) { d.InstallationID = "private\ninstallation" }} {
		d := valid
		mutate(&d)
		if d.Validate() != ErrInvalidDevice {
			t.Fatal("invalid device accepted")
		}
	}
}
func TestDeliveryResultRules(t *testing.T) {
	for _, tt := range []struct {
		result  ProviderResult
		attempt int
		want    string
	}{{ProviderResult{Code: "accepted"}, 3, Sent}, {ProviderResult{Code: "cancelled"}, 1, Cancelled}, {ProviderResult{Code: "unavailable", Temporary: true}, 1, Pending}, {ProviderResult{Code: "unavailable", Temporary: true}, 3, Failed}, {ProviderResult{Code: "invalid_argument"}, 1, Failed}} {
		if got := DeliveryStatus(tt.result, tt.attempt, 3); got != tt.want {
			t.Fatal(got, tt.want)
		}
	}
}
