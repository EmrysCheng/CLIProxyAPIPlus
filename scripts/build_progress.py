"""Structured, fixed-identifier source-build progress; never publish raw output."""
import os

STEPS = ('checking_tools', 'verifying_recipe', 'fetching_source', 'preparing_source',
         'frontend_dependencies', 'frontend_build', 'product_tests', 'binary_build',
         'packaging', 'single_port_test', 'update_test', 'recipe_test', 'frontend_tests',
         'adapter_tests', 'binary_smoke', 'maintenance_smoke', 'verifying_artifact')

def step(value):
    if value not in STEPS:
        raise ValueError('Unknown build progress step')
    if os.environ.get('PLUS_SOURCE_BUILD_PROGRESS') == '1':
        print('PLUS_SOURCE_PHASE:' + value, flush=True)
