package domain

// PricingSnapshot records the quote used at creation, never a live tariff reference.
type PricingSnapshot struct {
	SettingsVersion              int64  `json:"settings_version"`
	MinimumDeliverySubtotalMinor int64  `json:"minimum_delivery_subtotal_minor"`
	TariffID                     int64  `json:"tariff_id,omitempty"`
	MaxDistanceMeters            int64  `json:"max_distance_meters,omitempty"`
	DeliveryFeeMinor             int64  `json:"delivery_fee_minor"`
	DistanceMeters               *int64 `json:"distance_meters,omitempty"`
}

func clonePricing(p *PricingSnapshot) *PricingSnapshot {
	if p == nil {
		return nil
	}
	c := *p
	if p.DistanceMeters != nil {
		d := *p.DistanceMeters
		c.DistanceMeters = &d
	}
	return &c
}

// AmountToCollect also reads immutable historical creation responses without TotalMinor.
func (s State) AmountToCollect() int64 {
	if s.TotalMinor == 0 {
		return s.SubtotalMinor
	}
	return s.TotalMinor
}
