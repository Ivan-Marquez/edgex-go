/*******************************************************************************
 * Copyright 2017 Dell Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except
 * in compliance with the License. You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software distributed under the License
 * is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express
 * or implied. See the License for the specific language governing permissions and limitations under
 * the License.
 *******************************************************************************/
package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	types "github.com/edgexfoundry/edgex-go/internal/core/metadata/errors"
	"github.com/edgexfoundry/edgex-go/internal/core/metadata/operators/device"
	"github.com/edgexfoundry/edgex-go/internal/pkg/db"
	"github.com/edgexfoundry/go-mod-core-contracts/clients/notifications"
	"github.com/edgexfoundry/go-mod-core-contracts/models"
	"github.com/edgexfoundry/go-mod-core-contracts/requests/states/admin"
	"github.com/edgexfoundry/go-mod-core-contracts/requests/states/operating"
	"github.com/gorilla/mux"
)

func restGetAllDevices(w http.ResponseWriter, _ *http.Request) {
	op := device.NewDeviceLoadAll(Configuration.Service, dbClient, LoggingClient)
	devices, err := op.Execute()
	if err != nil {
		switch err.(type) {
		case types.ErrLimitExceeded:
			http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(&devices)
}

// Post a new device
// Attached objects (Addressable, Profile, Service) are referenced by ID or name
// 409 conflict if any of the attached items can't be found by ID or name
// Ignore everything else from the attached objects
func restAddNewDevice(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	var d models.Device
	err := json.NewDecoder(r.Body).Decode(&d)
	if err != nil {
		LoggingClient.Error(err.Error())
		status := http.StatusBadRequest
		if _, ok := err.(models.ErrContractInvalid); !ok {
			status = http.StatusInternalServerError
		}
		http.Error(w, err.Error(), status)
		return
	}

	ctx := r.Context()
	// The following requester instance is necessary because we will be making an HTTP call to the device service
	// associated with the new device in the Notifier below. There is no device service client. Additionally, the
	// requester interface should be mocked for unit testability and so is injected into the Notifier.
	requester, err := device.NewRequester(device.Http, LoggingClient, ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ch := make(chan device.DeviceEvent)
	defer close(ch)

	notifier := device.NewNotifier(ch, nc, Configuration.Notifications, dbClient, requester, LoggingClient, ctx)
	go notifier.Execute()

	op := device.NewAddDevice(ch, dbClient, d)
	newId, err := op.Execute()
	if err != nil {
		LoggingClient.Error(err.Error())
		switch err.(type) {
		case types.ErrDuplicateName:
			http.Error(w, err.Error(), http.StatusConflict)
		case types.ErrItemNotFound:
			http.Error(w, err.Error(), http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(newId))
}

// Update the device
// Use ID to identify device first, then name
// Can't create new Device Services/Profiles with a PUT, but you can reference another one
func restUpdateDevice(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	var rd models.Device
	err := json.NewDecoder(r.Body).Decode(&rd)
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	ch := make(chan device.DeviceEvent)
	defer close(ch)

	ctx := r.Context()

	requester, err := device.NewRequester(device.Http, LoggingClient, ctx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	notifier := device.NewNotifier(ch, nc, Configuration.Notifications, dbClient, requester, LoggingClient, ctx)
	go notifier.Execute()

	op := device.NewUpdateDevice(ch, dbClient, rd, LoggingClient)
	err = op.Execute()

	if err != nil {
		LoggingClient.Error(err.Error())
		switch err.(type) {
		case types.ErrDuplicateName:
			http.Error(w, err.Error(), http.StatusConflict)
		case types.ErrItemNotFound:
			http.Error(w, err.Error(), http.StatusNotFound)
		default:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("true"))
}

func restGetDevicesWithLabel(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	label, err := url.QueryUnescape(vars[LABEL])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	res, err := dbClient.GetDevicesWithLabel(label)
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func restGetDeviceByProfileId(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var pid string = vars[PROFILEID]

	// Check if the device profile exists
	_, err := dbClient.GetDeviceProfileById(pid)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		LoggingClient.Error(err.Error())
		return
	}

	res, err := dbClient.GetDevicesByProfileId(pid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		LoggingClient.Error(err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func restGetDeviceByServiceId(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var sid string = vars[SERVICEID]

	// Check if the device service exists
	_, err := dbClient.GetDeviceServiceById(sid)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		}
		LoggingClient.Error(err.Error())
		return
	}

	res, err := dbClient.GetDevicesByServiceId(sid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		LoggingClient.Error(err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// If the result array is empty, don't return http.NotFound, just return empty array
func restGetDeviceByServiceName(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	sn, err := url.QueryUnescape(vars[SERVICENAME])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Check if the device service exists
	ds, err := dbClient.GetDeviceServiceByName(sn)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		}
		LoggingClient.Error(err.Error())
		return
	}

	// Find devices by service ID now that you have the Service object (and therefor the ID)
	res, err := dbClient.GetDevicesByServiceId(ds.Id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		LoggingClient.Error(err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func restGetDeviceByProfileName(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	pn, err := url.QueryUnescape(vars[PROFILENAME])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Check if the device profile exists
	dp, err := dbClient.GetDeviceProfileByName(pn)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		}
		LoggingClient.Error(err.Error())
		return
	}

	// Use profile ID now that you have the profile object
	res, err := dbClient.GetDevicesByProfileId(dp.Id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		LoggingClient.Error(err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

func restGetDeviceById(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var did string = vars[ID]

	res, err := dbClient.GetDeviceById(did)
	if err != nil {
		LoggingClient.Error(err.Error())
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

//Shouldn't need "rest" in any of these methods. Adding it here for consistency right now.
func restCheckForDevice(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	token := vars[ID] //referring to this as "token" for now since the source variable is double purposed

	//Check for name first since we're using that meaning by default.
	dev, err := dbClient.GetDeviceByName(token)
	if err != nil {
		if err != db.ErrNotFound {
			LoggingClient.Error(err.Error())
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		} else {
			LoggingClient.Debug(fmt.Sprintf("device %s %v", token, err))
		}
	}

	//If lookup by name failed, see if we were passed the ID
	if len(dev.Name) == 0 {
		if dev, err = dbClient.GetDeviceById(token); err != nil {
			LoggingClient.Error(err.Error())
			if err == db.ErrNotFound {
				http.Error(w, err.Error(), http.StatusNotFound)
			} else if err == db.ErrInvalidObjectId {
				http.Error(w, err.Error(), http.StatusBadRequest)
			} else {
				http.Error(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(dev)
}

func decodeState(r *http.Request) (mode string, state string, err error) {
	var admin admin.UpdateRequest
	var ops operating.UpdateRequest

	bodyBytes, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return
	}

	var errMsg string
	decoder := json.NewDecoder(bytes.NewBuffer(bodyBytes))
	err = decoder.Decode(&admin)
	if err != nil {
		switch err := err.(type) {
		case models.ErrContractInvalid:
			errMsg = err.Error()
		default:
			return "", "", err
		}
	} else {
		return ADMINSTATE, string(admin.AdminState), nil
	}

	// In this case, the supplied request was not for the AdminState. Try OperatingState.
	decoder = json.NewDecoder(bytes.NewBuffer(bodyBytes))
	err = decoder.Decode(&ops)
	if err != nil {
		switch err := err.(type) {
		case models.ErrContractInvalid:
			errMsg += "; " + err.Error()
		default:
			return "", "", err
		}
	} else {
		return OPSTATE, string(ops.OperatingState), nil
	}

	// In this case, the request we were given in completely invalid
	return "", "", fmt.Errorf("unknown request type: data decode failed for both states: %v", errMsg)
}

func restSetDeviceStateById(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var did = vars[ID]
	updateMode, state, err := decodeState(r)
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check if the device exists
	d, err := dbClient.GetDeviceById(did)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		}
		LoggingClient.Error(err.Error())
		return
	}

	if err = updateDeviceState(updateMode, state, d); err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Notify
	notifyDeviceAssociates(d, http.MethodPut, r.Context())

	w.WriteHeader(http.StatusOK)
}

func restSetDeviceStateByDeviceName(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	n, err := url.QueryUnescape(vars[NAME])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	updateMode, state, err := decodeState(r)
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check if the device exists
	d, err := dbClient.GetDeviceByName(n)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		LoggingClient.Error(err.Error())
		return
	}

	if err = updateDeviceState(updateMode, state, d); err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	// Notify
	notifyDeviceAssociates(d, http.MethodPut, ctx)

	w.WriteHeader(http.StatusOK)
}

func updateDeviceState(updateMode string, state string, d models.Device) error {
	switch updateMode {
	case ADMINSTATE:
		d.AdminState = models.AdminState(strings.ToUpper(state))
	case OPSTATE:
		d.OperatingState = models.OperatingState(strings.ToUpper(state))
	}
	return dbClient.UpdateDevice(d)
}

func restDeleteDeviceById(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var did string = vars[ID]

	// Check if the device exists
	d, err := dbClient.GetDeviceById(did)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		LoggingClient.Error(err.Error())
		return
	}

	ctx := r.Context()
	if err := deleteDevice(d, w, ctx); err != nil {
		LoggingClient.Error(err.Error())
		return
	}

	w.Write([]byte("true"))
}

func restDeleteDeviceByName(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	n, err := url.QueryUnescape(vars[NAME])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check if the device exists
	d, err := dbClient.GetDeviceByName(n)
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	ctx := r.Context()
	if err := deleteDevice(d, w, ctx); err != nil {
		LoggingClient.Error(err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("true"))
}

// Delete the device
func deleteDevice(d models.Device, w http.ResponseWriter, ctx context.Context) error {
	if err := deleteAssociatedReportsForDevice(d, w); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return err
	}

	if err := dbClient.DeleteDeviceById(d.Id); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return err
	}

	// Notify Associates
	if err := notifyDeviceAssociates(d, http.MethodDelete, ctx); err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return err
	}

	return nil
}

// Delete the associated device reports for the device
func deleteAssociatedReportsForDevice(d models.Device, w http.ResponseWriter) error {
	reports, err := dbClient.GetDeviceReportByDeviceName(d.Name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		LoggingClient.Error(err.Error())
		return err
	}

	// Delete the associated reports
	for _, report := range reports {
		if err := dbClient.DeleteDeviceReportById(report.Id); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			LoggingClient.Error(err.Error())
			return err
		}
		notifyDeviceReportAssociates(report, http.MethodDelete)
	}

	return nil
}

func restSetDeviceLastConnectedById(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var did string = vars[ID]
	var vlc string = vars[LASTCONNECTED]
	lc, err := strconv.ParseInt(vlc, 10, 64)
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check if the device exists
	d, err := dbClient.GetDeviceById(did)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		LoggingClient.Error(err.Error())
		return
	}

	ctx := r.Context()
	// Update last connected
	if err = setLastConnected(d, lc, false, w, ctx); err != nil {
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("true"))
}

func restSetLastConnectedByIdNotify(w http.ResponseWriter, r *http.Request) {
	// Get the URL parameters
	vars := mux.Vars(r)
	var did = vars[ID]
	var vlc = vars[LASTCONNECTED]
	notify, err := strconv.ParseBool(vars[LASTCONNECTEDNOTIFY])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	lc, err := strconv.ParseInt(vlc, 10, 64)
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check if the device exists
	d, err := dbClient.GetDeviceById(did)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		LoggingClient.Error(err.Error())
		return
	}

	ctx := r.Context()
	// Update last connected
	if err = setLastConnected(d, lc, notify, w, ctx); err != nil {
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("true"))
}

func restSetDeviceLastConnectedByName(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	n, err := url.QueryUnescape(vars[NAME])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	var vlc string = vars[LASTCONNECTED]
	lc, err := strconv.ParseInt(vlc, 10, 64)
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Check if the device exists
	d, err := dbClient.GetDeviceByName(n)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		}
		LoggingClient.Error(err.Error())
		return
	}

	ctx := r.Context()
	// Update last connected
	if err = setLastConnected(d, lc, false, w, ctx); err != nil {
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("true"))
}

func restSetDeviceLastConnectedByNameNotify(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	n, err := url.QueryUnescape(vars[NAME])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	var vlc string = vars[LASTCONNECTED]
	lc, err := strconv.ParseInt(vlc, 10, 64)
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	notify, err := strconv.ParseBool(vars[LASTCONNECTEDNOTIFY])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Check if the device exists
	d, err := dbClient.GetDeviceByName(n)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		}
		LoggingClient.Error(err.Error())
		return
	}

	ctx := r.Context()
	// Update last connected
	if err = setLastConnected(d, lc, notify, w, ctx); err != nil {
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("true"))
}

// Update the last connected value for the device
func setLastConnected(d models.Device, time int64, notify bool, w http.ResponseWriter, ctx context.Context) error {
	d.LastConnected = time
	if err := dbClient.UpdateDevice(d); err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return err
	}

	if notify {
		notifyDeviceAssociates(d, http.MethodPut, ctx)
	}

	return nil
}

func restSetDeviceLastReportedById(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var did string = vars[ID]
	var vlr string = vars[LASTREPORTED]
	lr, err := strconv.ParseInt(vlr, 10, 64)
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Check if the device exists
	d, err := dbClient.GetDeviceById(did)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		}
		LoggingClient.Error(err.Error())
		return
	}

	ctx := r.Context()
	// Update Last Reported
	if err = setLastReported(d, lr, false, w, ctx); err != nil {
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("true"))
}

func restSetDeviceLastReportedByIdNotify(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	var did string = vars[ID]
	var vlr string = vars[LASTREPORTED]
	lr, err := strconv.ParseInt(vlr, 10, 64)
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	notify, err := strconv.ParseBool(vars[LASTREPORTEDNOTIFY])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	// Check if the device exists
	d, err := dbClient.GetDeviceById(did)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
		}
		LoggingClient.Error(err.Error())
		return
	}

	ctx := r.Context()
	// Update last reported
	if err = setLastReported(d, lr, notify, w, ctx); err != nil {
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("true"))
}

func restSetDeviceLastReportedByName(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	n, err := url.QueryUnescape(vars[NAME])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var vlr string = vars[LASTREPORTED]
	lr, err := strconv.ParseInt(vlr, 10, 64)
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check if the device exists
	d, err := dbClient.GetDeviceByName(n)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		LoggingClient.Error(err.Error())
		return
	}

	ctx := r.Context()
	// Update last reported
	if err = setLastReported(d, lr, false, w, ctx); err != nil {
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("true"))
}

func restSetDeviceLastReportedByNameNotify(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	n, err := url.QueryUnescape(vars[NAME])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var vlr string = vars[LASTREPORTED]
	lr, err := strconv.ParseInt(vlr, 10, 64)
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	notify, err := strconv.ParseBool(vars[LASTREPORTEDNOTIFY])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Check if the device exists
	d, err := dbClient.GetDeviceByName(n)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		LoggingClient.Error(err.Error())
		return
	}

	ctx := r.Context()
	// Update last reported
	if err = setLastReported(d, lr, notify, w, ctx); err != nil {
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("true"))
}

// Update the last reported field of the device
func setLastReported(d models.Device, time int64, notify bool, w http.ResponseWriter, ctx context.Context) error {
	d.LastReported = time
	if err := dbClient.UpdateDevice(d); err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return err
	}

	if notify {
		notifyDeviceAssociates(d, http.MethodPut, ctx)
	}

	return nil
}

func restGetDeviceByName(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	dn, err := url.QueryUnescape(vars[NAME])
	if err != nil {
		LoggingClient.Error(err.Error())
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	res, err := dbClient.GetDeviceByName(dn)
	if err != nil {
		if err == db.ErrNotFound {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		LoggingClient.Error(err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// Notify the associated device service for the device
func notifyDeviceAssociates(d models.Device, action string, ctx context.Context) error {
	// Post the notification to the notifications service
	postNotification(d.Name, action, ctx)

	// Callback for device service
	ds, err := dbClient.GetDeviceServiceById(d.Service.Id)
	if err != nil {
		LoggingClient.Error(err.Error())
		return err
	}
	if err := notifyAssociates([]models.DeviceService{ds}, d.Id, action, models.DEVICE); err != nil {
		LoggingClient.Error(err.Error())
		return err
	}

	return nil
}

func postNotification(name string, action string, ctx context.Context) {
	// Only post notification if the configuration is set
	if Configuration.Notifications.PostDeviceChanges {
		// Make the notification
		notification := notifications.Notification{
			Slug:        Configuration.Notifications.Slug + strconv.FormatInt(db.MakeTimestamp(), 10),
			Content:     Configuration.Notifications.Content + name + "-" + string(action),
			Category:    notifications.SW_HEALTH,
			Description: Configuration.Notifications.Description,
			Labels:      []string{Configuration.Notifications.Label},
			Sender:      Configuration.Notifications.Sender,
			Severity:    notifications.NORMAL,
		}

		nc.SendNotification(notification, ctx)
	}
}
