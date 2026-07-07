package tools

import (
	"context"
	"fmt"
	"strings"

	appie "github.com/gwillem/appie-go"
)

// The AH app keeps the shopping basket (winkelmand) in GraphQL; the legacy
// order/v1 endpoints only see a cart once an actual order exists (delivery
// slot chosen or order reopened). Without one they fail with "Order does not
// exist". These helpers let the cart tools fall back to the GraphQL basket so
// they also work on the plain app basket.

const basketQuery = `query Basket {
  basket {
    id
    items {
      id
      quantity
      product {
        id
        title
        priceV2 { now { amount } }
      }
    }
  }
}`

type basketQL struct {
	Basket struct {
		ID    string `json:"id"`
		Items []struct {
			ID       int `json:"id"`
			Quantity int `json:"quantity"`
			Product  *struct {
				ID      int    `json:"id"`
				Title   string `json:"title"`
				PriceV2 struct {
					Now struct {
						Amount float64 `json:"amount"`
					} `json:"now"`
				} `json:"priceV2"`
			} `json:"product"`
		} `json:"items"`
	} `json:"basket"`
}

func fetchBasketQL(ctx context.Context, c *appie.Client) (*basketQL, error) {
	var b basketQL
	if err := c.DoGraphQL(ctx, basketQuery, nil, &b); err != nil {
		return nil, fmt.Errorf("get basket: %w", err)
	}
	return &b, nil
}

const basketUpdateMutation = `mutation UpdateBasket($items: [BasketMutation!]!) {
  basketItemsUpdate(items: $items) { status }
}`

// updateBasketQL sets product quantities in the GraphQL basket (0 removes).
func updateBasketQL(ctx context.Context, c *appie.Client, quantities map[int]int) error {
	items := make([]map[string]any, 0, len(quantities))
	for id, q := range quantities {
		items = append(items, map[string]any{"id": id, "quantity": q})
	}
	var resp struct {
		BasketItemsUpdate struct {
			Status string `json:"status"`
		} `json:"basketItemsUpdate"`
	}
	if err := c.DoGraphQL(ctx, basketUpdateMutation, map[string]any{"items": items}, &resp); err != nil {
		return fmt.Errorf("update basket: %w", err)
	}
	if resp.BasketItemsUpdate.Status != "SUCCESS" {
		return fmt.Errorf("update basket: status %s", resp.BasketItemsUpdate.Status)
	}
	return nil
}

// isNoActiveOrder reports whether an error means there is no active legacy
// order — the situation where only the GraphQL app basket exists.
func isNoActiveOrder(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "order does not exist")
}
