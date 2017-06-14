var http = require('http');
var server = http.createServer(function(req, resp) {
  resp.end('Hello world');
});
server.listen(8080);
