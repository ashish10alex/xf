declare({ database: "raw-project", schema: "raw", name: "events" });

publish("js_view", { type: "view" }).query(
  (ctx) => `SELECT * FROM ${ctx.ref("events")}`
);
