# Recsplain lite
A trimmed down, scalable version of [Recsplain](https://github.com/argmaxml/recsplain) written in Go.

For more details on Recplain, See our [Getting Started Guide](https://recsplain.readthedocs.io/en/latest/)
## Differences from Recsplain
### Performance optimization
The recsplain lite version is written in `golang` for performance, and relies on the `fiber` framework.

In addition to the stack-change, Recsplain Lite also incorparates caching techniques and precomptues the schema at startup.
### Partial list of encoders
Recsplain Lite only supports these 2 encoders:

    1. Numeric Encoder - using a numeric value sepcified in the requsest `as is`
    1. Numpy Encoder - specifying the embedding from a `.npy` file
    
### Schema changes
 Recsplain Lite precomputes the schema and variants at startup, so schema changes are not supported in runtime.
