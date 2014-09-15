/**
 * wsFile client
 *
 * Socket constructor & handler
 *
 * @author herenow
 */

define(function()
{
    'use strict';

     function WsFile(url) {
        this.url = url

        // Connect
        Connect.call(this)

        this.counter = 0
        this.callbacks = []
        this.buffers = []
    }

    function Connect() {
        this.ws = new WebSocket(this.url)
        this.ws.binaryType = "arraybuffer"
        this.ws.onopen = OnOpen.bind(this)
        this.ws.onclose = OnClose.bind(this)
        this.ws.onmessage = OnReceiveAsync.bind(this)
    }

    function OnOpen() {
        console.log("wsFile open!")
    }

    function OnClose() {
        console.log("wsFile close!")

        // Re-connect
        setTimeout(Connect.bind(this), 1000)
    }

    function OnReceiveAsync(event) {
        var data = event.data
        var header_size = 4
        var view   = new DataView(data, 0, header_size)
        var chan_id = view.getUint16(0, false)
        var seq = view.getUint16(2, false)
        var payload = data.slice(header_size)

        console.log('chan_id ' + chan_id + ' seq ' + seq + ' payload_len ' + payload.byteLength)

        // If empty payload, file transfer has finished
        if(payload.byteLength === 0 && typeof this.buffers[chan_id] !== "undefined") {
            this.callbacks[chan_id]( this.buffers[chan_id].buffer )
            this.callbacks[chan_id] = null // clear
            this.buffers[chan_id] = null // clear
        }
        // Check if buffer was already created, if not, initiate
        else if(typeof this.buffers[chan_id] === "undefined") {
            this.buffers[chan_id] = new Uint8Array(payload);
        }
        // Else, append to channel buffer
        else {
            var buffer = this.buffers[chan_id]
            var _data = new Uint8Array( buffer.length + payload.byteLength );
            var _payload = new Uint8Array(payload)
            _data.set( buffer, 0 );
            _data.set( _payload, buffer.length );
            this.buffers[chan_id] = _data;
        }
    }

    function OnReceiveSync(event) {
        var data = event.data
        var seq  = this.seq
        var ws   = this.self.ws[seq]

        console.log("Received data from seq " + seq + " of length: " + data.byteLength)

        // If empty payload, file transfer has finished
        if(data.byteLength === 0) {
            console.log("finished seq " + seq)
            ws.callback( ws.buffer )
            ws.callback = null // clear
            ws.buffer = null // clear
        }
        // Check if buffer was already created, if not, initiate
        else if(ws.buffer === null) {
            ws.buffer = new Uint8Array(data);
        }
        // Else, append to channel buffer
        else {
            var buffer = ws.buffer
            var _data = new Uint8Array( buffer.length + data.byteLength );
            _data.set( buffer, 0 );
            _data.set( data, buffer.length );
            ws.buffer = _data;
        }
    }

    WsFile.prototype.get = function Get(url, callback) {
        this.ws.send("GET " + this.counter + " " + url)
        this.callbacks[this.counter] = callback
        this.counter++
    }

    var seq = 0;

    WsFile.prototype.getSync = function Get(url, callback) {
        this.ws[seq].send("GET " + url)
        this.ws[seq].callback = callback

        seq++
    }

    /**
     * Export
     */
    return WsFile;
});

