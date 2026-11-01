publish("broken", { type: "table" }).query(ctx => `SELECT ${undefinedVariable}`);
throw new Error("deliberate error in bad_js.js");
