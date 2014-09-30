package main

import (
	"flag"
	"text/template"
	"io/ioutil"
	"log"
	"net"
	"fmt"
	"net/http"
//	"github.com/fsouza/go-dockerclient"
	"time"
	"strconv"
	"github.com/franela/goreq"
)

const (
	ENABLE_PAGER_DUTY = true

	// Raise an alarm if the temperature exceeds this
	ALARM_TRIGGER_TEMPERATURE = 30
	ALARM_RESOLVE_TEMPERATURE = 27

	// Raise an alarm if we don't get an update for this long
	ALARM_NON_REPORTING_TIME = 600

	// Graph URL
	REPORT_URL = "http://localhost:3000/temperature/view"

	// Event types
	ALARM_TEMPERATURE_EXCEEDED = 1
	ALARM_NOT_REPORTING = 2
	ALARM_NONE = 3
)

var (
	addr = flag.Bool("addr", false, "find open address and print to final-port.txt")
)

var templates = template.Must(template.ParseFiles("viewTemperature.html"))

type Temperature struct {
	when	int64 // seconds since 1970
    temp 	float64
}

var temps [5 * 60 * 2]Temperature // 5 hours, twice a minute
var first = -1 // first
var next = 0 // latest
var lastUpdateTime = time.Now().Unix()
var alarmLevel = 0

func saveTemperature(w http.ResponseWriter, r *http.Request) {
    // p, err := loadPage(title)
    // if err != nil {
    //     http.Redirect(w, r, "/edit/"+title, http.StatusFound)
    //     return
    // }
    // renderTemplate(w, "view", p)
	
	//fmt.Println("got:", r.URL.Query());
	param_t := r.URL.Query()["t"][0]; // assuming it’s like /temperature/save?t=12.34
	t, err := strconv.ParseFloat(param_t, 32)
	if err != nil {
		fmt.Fprintf(w, "Invalid parameter 't'")
		return
	}
	
	now := time.Now().Unix()
	temp := Temperature{ now, t }
	
	// Check we are't wrapping around onto old data
//	temp := 123.456
	if first < 0 {
		// Empty list
		temps[0] = temp
		first = 0
		next = 1
	} else if next == first {
		// Full list
		temps[next] = temp
		first = (first + 1) % len(temps)
		next = first
	} else {
		temps[next] = temp
		next = (next + 1) % len(temps)
	}
	lastUpdateTime = now
	//fmt.Println("Called saveTemperature ", temps, first, next)
	fmt.Fprintf(w, "ok\n")
}

func viewTemperature(w http.ResponseWriter, r *http.Request) {
    // p, err := loadPage(title)
    // if err != nil {
    //     http.Redirect(w, r, "/edit/"+title, http.StatusFound)
    //     return
    // }
    // renderTemplate(w, "view", p)
	fmt.Println("Called viewTemperature")
	
	if first < 0 {
		// No temperatures yet
		fmt.Fprintf(w, "No temperatures yet.\n")
		
	} else {
		// Display the temperatures
		numTemps := 0
		switch {
		case first < 0: numTemps = 0
		case next == first: numTemps = len(temps)
		default: numTemps = next - first
		}
		//fmt.Printf("Have %d temperatures\n", numTemps)
		
		type Sample struct {
		    When string
		    Temp float64
		}
		
		
		slice := make([]Sample, numTemps, numTemps)
		cnt := 0
		for i := first ; ; {
			
			// 2014,10,3,12,45,0
			sampleTime := time.Unix(temps[i].when, 0)
			//fmt.Println(sampleTime)
//			timeStr := sampleTime.Format("yyyy,mm,dd,hh,mm,ss")
			timeStr := sampleTime.Format("2006,01,02,15,04,05")
			//fmt.Println(timeStr)
			
			slice[cnt] = Sample { timeStr, temps[i].temp }
			cnt = cnt + 1
//			fmt.Fprintf(w, "%d: %f<br>\n", i, temps[i].temp)
			i = (i + 1) % len(temps)
			if i == next {
				break;
			}
		}
		//fmt.Println("---------------")
		//fmt.Println(slice)
		/*
		fmt.Fprintf(w, "<br><b>Alarm level %d.</b>", alarmLevel)
		*/
	    // renderTemplate(w, "viewTemperature", slice)
	    err := templates.ExecuteTemplate(w, "viewTemperature.html", slice)
	    if err != nil {
	        http.Error(w, err.Error(), http.StatusInternalServerError)
	    }
		
	    //
	    // err := templates.ExecuteTemplate(w, tmpl+".html", p)
	    // if err != nil {
	    //     http.Error(w, err.Error(), http.StatusInternalServerError)
	    // }
		
	}
	
}

func checkTemperature() {
	now := time.Now().Unix()
	
	fmt.Println("checkTemperature ", now, lastUpdateTime)
	
	if first < 0 {
		// No reporting yet
		return
	}
	pos := next - 1
	if pos < 0 {
		pos = len(temps) - 1
	}
	temp := temps[pos]
	fmt.Println("temp=", temp)
	lastUpdateTime2 := temp.when
	lastTemperature := temp.temp
	
	fmt.Println(lastUpdateTime, lastUpdateTime2)

	timeSinceUpdate := now - lastUpdateTime
	
	// Check the time since the last update
	newLevel := 0
	alarmType := ALARM_NONE
	switch {
		
	case timeSinceUpdate > ALARM_NON_REPORTING_TIME:
		// Monitor is not reporting
		fmt.Println(" -> NOT BEING UPDATED.\n")
		newLevel = 1
		alarmType = ALARM_NOT_REPORTING

	case lastTemperature > ALARM_TRIGGER_TEMPERATURE:
		// High temperature
		fmt.Println(" -> EXCESSIVE TEMPERATURE.\n")
		newLevel = 1
		alarmType = ALARM_TEMPERATURE_EXCEEDED

	case alarmLevel > 0 && lastTemperature > ALARM_RESOLVE_TEMPERATURE:
		// Don't reset the alarm until it drops below this temperature
		fmt.Println(" -> TEMPERATURE STILL HIGH.\n")
		newLevel = alarmLevel
		alarmType = ALARM_TEMPERATURE_EXCEEDED
		
	default:
		newLevel = 0
		alarmType = ALARM_NONE;
	}
	
	// Tell someone if the alarm level has changed
	if alarmLevel != newLevel {
		fmt.Printf("********** Alarm level changed to %d\n", newLevel)
		alarmLevel = newLevel

		// If the alarm level has changed, let PagerDuty.com know
		pagerDuty_event(alarmType)
	}
}

func pagerDuty_event(alarmType int) {
	
	fmt.Println("Trigger pagerduty.com")	
	
	if !ENABLE_PAGER_DUTY {
		fmt.Println("Pager duty is not enabled.")	
		return;
	}
	
	// Decide what we'll send to pagerduty.com
	body := ""
	switch alarmType {
	case ALARM_TEMPERATURE_EXCEEDED:	
		body = `
		{
			"service_key": "735da79b2b9a427782f1aa0b6c16a9dd",
			"incident_key": "serverRoom/temperature",
			"event_type": "trigger",
			"description": "Room is hot",
			"client": "Temperature Monitoring Service",
			"client_url": "http://localhost:3000/temperature/view",
			"details": {
			}
		}`
	case ALARM_NOT_REPORTING:	
		body = `
		{
			"service_key": "735da79b2b9a427782f1aa0b6c16a9dd",
			"incident_key": "serverRoom/temperature",
			"event_type": "trigger",
			"description": "Monitor has stopped reporting",
			"client": "Temperature Monitoring Service",
			"client_url": "http://localhost:3000/temperature/view",
			"details": {
			}
		}`
	case ALARM_NONE:	
		body = `
		{
			"service_key": "735da79b2b9a427782f1aa0b6c16a9dd",
			"incident_key": "serverRoom/temperature",
			"event_type": "resolve",
			"description": "Reporting acceptable temperature",
			"client": "Temperature Monitoring Service",
			"client_url": "http://localhost:3000/temperature/view",
			"details": {
			}
		}`
	}
	
	uri :=  "https://events.pagerduty.com/generic/2010-04-15/create_event.json"
	fmt.Printf("\nContacting Pager Duty:\n  url = %s\n  body = %s\n", uri, body)
	
	goreq.SetConnectTimeout(10000 * time.Millisecond)
	res, err := goreq.Request{
	    Method: "POST",
	    Uri: uri,
	    Body: body,
		ContentType: "application/json",
//		Accept: "application/json",
//		UserAgent: "goreq",
		Timeout: 10000 * time.Millisecond,
	}.Do()
	
	fmt.Println("\nReply:")
	if err == nil {
		fmt.Println("  status =", res.StatusCode)
		fmt.Println("  contentType =", res.Header.Get("Content-Type"))
		if res.Body != nil {
			// fmt.Println("Body=", res.Body)
			body, err := res.Body.ToString()
			fmt.Println("  body =", body)
			fmt.Println("  error =", err)
		}
		defer res.Body.Close()
	} else {
		fmt.Println("  error =", err.Error())
	}
}

func main() {
	
	// pagerDuty_event(ALARM_NONE)
	// return
	
	fmt.Println("Server starting")
	flag.Parse()
    // p1 := &Page{Title: "TestPage", Body: []byte("This is a sample Page.")}
    // p1.save()
    // p2, _ := loadPage("TestPage")
    // fmt.Println(string(p2.Body))
    http.HandleFunc("/temperature/save", saveTemperature)
    http.HandleFunc("/temperature/view", viewTemperature)
	
	if *addr {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatal(err)
		}
		err = ioutil.WriteFile("final-port.txt", []byte(l.Addr().String()), 0644)
		if err != nil {
			log.Fatal(err)
		}
		s := &http.Server{}
		s.Serve(l)
		return
	}
	
/*	
	
    // endpoint := "unix:///var/run/docker.sock"
	endpoint := "tcp://192.168.59.103:2375"
    client, _ := docker.NewClient(endpoint)
	
	fmt.Println("\n\nIMAGES:")
    imgs, _ := client.ListImages(true)
    for _, img := range imgs {
			if img.RepoTags[0] != "<none>:<none>" {
				fmt.Println()
	            fmt.Println("RepoTags: ", img.RepoTags)
	            // fmt.Println("ID: ", img.ID)
	            fmt.Println("Created: ", img.Created)
	            fmt.Println("Size: ", img.Size)
	            fmt.Println("VirtualSize: ", img.VirtualSize)
	            // fmt.Println("ParentId: ", img.ParentId)
			}
    }
	
	fmt.Println("\n\nCONTAINERS:")
    containers, _ := client.ListContainers(docker.ListContainersOptions{})
    for _, img := range containers {
        fmt.Println("ID: ", img.ID)
        fmt.Println("Container: ", img)
			/*
            fmt.Println("RepoTags: ", img.RepoTags)
            fmt.Println("Created: ", img.Created)
            fmt.Println("Size: ", img.Size)
            fmt.Println("VirtualSize: ", img.VirtualSize)
            fmt.Println("ParentId: ", img.ParentId)
			* /
    }
*/

	
	// Check periodically whether the temperature has been updated
	ticker := time.NewTicker(20 * time.Second)
	go func() {
	    for {
	       select {
	        case <- ticker.C:
				checkTemperature()
	        }
	    }
	 }()
	
	
    http.ListenAndServe(":5000", nil)
}
