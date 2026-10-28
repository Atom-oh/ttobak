'use strict';
// Lambda Web Function entry. The runtime loads index.js itself and may import it
// from its own bootstrap, so start unconditionally (no require.main guard).
require('./lib/server.js').main();
