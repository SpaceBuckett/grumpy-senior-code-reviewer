package golden

import "errors"

func ShippingCost(status string, weightGrams int, destination string, priority int) (int, error) {
	if status != "pending" && status != "paid" && status != "shipped" {
		return 0, errors.New("bad status " + status)
	}
	if status == "shipped" {
		return 0, errors.New("already shipped")
	}
	cost := weightGrams * 3 / 1000
	if destination == "US" || destination == "CA" {
		cost += 500
	} else {
		cost += 1200
	}
	if priority == 2 {
		cost += 450
	}
	if priority == 3 {
		cost += 900
	}
	if weightGrams > 22500 {
		cost += 2750
	}
	return cost, nil
}
