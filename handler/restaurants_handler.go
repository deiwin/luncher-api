package handler

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"gopkg.in/mgo.v2"
	"gopkg.in/mgo.v2/bson"

	"github.com/Lunchr/luncher-api/db"
	"github.com/Lunchr/luncher-api/db/model"
	"github.com/Lunchr/luncher-api/router"
	"github.com/Lunchr/luncher-api/session"
	"github.com/Lunchr/luncher-api/storage"
	"github.com/deiwin/facebook"
	fbmodel "github.com/deiwin/facebook/model"
	"github.com/julienschmidt/httprouter"
)

// Restaurants returns a list of all restaurants
func Restaurants(restaurantsCollection db.Restaurants) router.Handler {
	return func(w http.ResponseWriter, r *http.Request) *router.HandlerError {
		restaurants, err := restaurantsCollection.Get()
		if err != nil {
			return router.NewHandlerError(err, "", http.StatusInternalServerError)
		}
		return writeJSON(w, restaurants)
	}
}

// Restaurant returns a router.Handler that returns the restaurant information for the specified restaurant
func Restaurant(c db.Restaurants, sessionManager session.Manager, users db.Users, fbAuth facebook.Authenticator) router.HandlerWithParams {
	handler := func(w http.ResponseWriter, r *http.Request, user *model.User, restaurant *model.Restaurant) *router.HandlerError {
		return writeJSON(w, restaurant)
	}
	return forRestaurant(sessionManager, users, c, handler, fbAuth)
}

// Restaurant returns a router.Handler that returns the restaurant information for the
// restaurant linked to the currently logged in user
func PostRestaurants(c db.Restaurants, sessionManager session.Manager, users db.Users) router.Handler {
	handler := func(w http.ResponseWriter, r *http.Request, user *model.User) *router.HandlerError {
		restaurant, err := parseRestaurant(r)
		if err != nil {
			return router.NewHandlerError(err, "Failed to parse the restaurant", http.StatusBadRequest)
		} else if restaurant.FacebookPageID == "" {
			return router.NewHandlerError(err, "Registering without an assciated FB page is currently disabled", http.StatusBadRequest)
		}
		insertedRestaurants, err := c.Insert(restaurant)
		if err != nil {
			return router.NewHandlerError(err, "Failed to store the restaurant in the DB", http.StatusInternalServerError)
		}
		var insertedRestaurant = insertedRestaurants[0]
		user.RestaurantIDs = append(user.RestaurantIDs, insertedRestaurant.ID)
		err = users.Update(user.FacebookUserID, user)
		if err != nil {
			// TODO: revert the restaurant insertion we just did? Look into mgo's txn package
			return router.NewHandlerError(err, "Failed to store the restaurant in the DB", http.StatusInternalServerError)
		}
		// TODO Move to after registering instead of after submitting a restaurant
		// We're using different handlers for login/register. Force logout to make sure all users are using login handler
		// to do things
		if err := users.UnsetSessionID(user.ID); err != nil {
			return router.NewHandlerError(err, "Failed to invalidate session ID", http.StatusInternalServerError)
		}
		return writeJSON(w, insertedRestaurant)
	}
	return checkLogin(sessionManager, users, handler)
}

// RestaurantOffers returns all upcoming offers for the restaurant linked to the
// currently logged in user
func RestaurantOffers(restaurants db.Restaurants, sessionManager session.Manager, users db.Users, offers db.Offers,
	imageStorage storage.Images, regions db.Regions, fbAuth facebook.Authenticator) router.HandlerWithParams {
	handler := func(w http.ResponseWriter, r *http.Request, user *model.User, restaurant *model.Restaurant) *router.HandlerError {
		region, err := regions.GetName(restaurant.Region)
		if err != nil {
			return router.NewHandlerError(err, "Failed to find the region for this restaurant", http.StatusInternalServerError)
		}
		timeLocation, err := time.LoadLocation(region.Location)
		if err != nil {
			return router.NewHandlerError(err, "The location of this region is misconfigured", http.StatusInternalServerError)
		}
		today, _ := getTodaysTimeRange(timeLocation)
		offers, err := offers.GetForRestaurant(restaurant.ID, today)
		if err != nil {
			return router.NewHandlerError(err, "Failed to find upcoming offers for this restaurant", http.StatusInternalServerError)
		}
		offerJSONs, handlerErr := mapOffersToJSON(offers, imageStorage)
		if handlerErr != nil {
			return handlerErr
		}
		return writeJSON(w, offerJSONs)
	}
	return forRestaurant(sessionManager, users, restaurants, handler, fbAuth)
}

type HandlerWithRestaurant func(w http.ResponseWriter, r *http.Request, user *model.User,
	restaurant *model.Restaurant) *router.HandlerError

func forRestaurant(sessionManager session.Manager, users db.Users, restaurants db.Restaurants,
	handler HandlerWithRestaurant, fbAuth facebook.Authenticator) router.HandlerWithParams {
	handlerWithUser := func(w http.ResponseWriter, r *http.Request, ps httprouter.Params, user *model.User) *router.HandlerError {
		restaurantIDString := ps.ByName("id")
		if restaurantIDString == "" {
			return router.NewSimpleHandlerError("Expected a restaurant ID to be specified", http.StatusBadRequest)
		} else if !bson.IsObjectIdHex(restaurantIDString) {
			return router.NewSimpleHandlerError("Invalid restaurant ID", http.StatusBadRequest)
		}
		restaurantID := bson.ObjectIdHex(restaurantIDString)
		restaurant, err := restaurants.GetID(restaurantID)
		if err == mgo.ErrNotFound {
			return router.NewHandlerError(err, "Failed to find the specified restaurant", http.StatusNotFound)
		} else if err != nil {
			return router.NewHandlerError(err, "Something went wrong while trying to find the specified restaurant", http.StatusInternalServerError)
		}
		if !idsInclude(user.RestaurantIDs, restaurantID) {
			fbPages, err := getPages(&user.Session.FacebookUserToken, fbAuth)
			if err != nil {
				return router.NewHandlerError(err, "Couldn't get the list of pages managed by this user", http.StatusBadGateway)
			} else if !fbPagesInclude(fbPages, restaurant.FacebookPageID) {
				return router.NewSimpleHandlerError("Not authorized to access this restaurant", http.StatusForbidden)
			}
		}
		return handler(w, r, user, restaurant)
	}
	return checkLoginWithParams(sessionManager, users, handlerWithUser)
}

func idsInclude(ids []bson.ObjectId, id bson.ObjectId) bool {
	for i := range ids {
		if ids[i] == id {
			return true
		}
	}
	return false
}

func fbPagesInclude(fbPages []fbmodel.Page, fbPageID string) bool {
	for _, page := range fbPages {
		if page.ID == fbPageID {
			return true
		}
	}
	return false
}

type HandlerWithParamsWithRestaurant func(w http.ResponseWriter, r *http.Request, ps httprouter.Params, user *model.User,
	restaurant *model.Restaurant) *router.HandlerError

func forRestaurantWithParams(sessionManager session.Manager, users db.Users, restaurants db.Restaurants,
	handler HandlerWithParamsWithRestaurant) router.HandlerWithParams {
	handlerWithUser := func(w http.ResponseWriter, r *http.Request, ps httprouter.Params, user *model.User) *router.HandlerError {
		restaurant, err := restaurants.GetID(user.RestaurantIDs[0])
		if err != nil {
			return router.NewHandlerError(err, "Failed to find the restaurant connected to this user", http.StatusInternalServerError)
		}
		return handler(w, r, ps, user, restaurant)
	}
	return checkLoginWithParams(sessionManager, users, handlerWithUser)
}

func parseRestaurant(r *http.Request) (*model.Restaurant, error) {
	var restaurant model.Restaurant
	err := json.NewDecoder(r.Body).Decode(&restaurant)
	if err != nil {
		return nil, err
	}
	// Add default values for configurable fields
	if restaurant.DefaultGroupPostMessageTemplate == "" {
		restaurant.DefaultGroupPostMessageTemplate = "Tänased päevapakkumised on:"
	}
	// XXX please look away, this is a hack
	if strings.Contains(strings.ToLower(restaurant.Address), "tartu") {
		restaurant.Region = "Tartu"
	} else {
		restaurant.Region = "Tallinn"
		// yup ...
	}
	return &restaurant, nil
}
